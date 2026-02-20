package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/Parsons-ai/comms-server/internal/identity"
	"github.com/Parsons-ai/comms-server/internal/store"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// setupAPI creates a test server and returns it along with the DB and a
// registered client identity that can be used to sign authenticated requests.
func setupAPI(t *testing.T) (*httptest.Server, *store.DB, *identity.Identity) {
	t.Helper()
	db, err := store.Open(":memory:", testLogger())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	id, _ := identity.Generate()
	srv := New("127.0.0.1:0", db, id, testLogger())
	ts := httptest.NewServer(srv.Handler())

	// Create a client identity and register it
	clientID, _ := identity.Generate()
	body, _ := json.Marshal(map[string]string{
		"public_key":   clientID.PublicKeyHex(),
		"display_name": "TestClient",
		"role":         "admin",
	})
	resp, err := ts.Client().Post(ts.URL+"/api/v1/users", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("register test client: %v", err)
	}
	resp.Body.Close()

	t.Cleanup(func() {
		ts.Close()
		db.Close()
	})
	return ts, db, clientID
}

// authGet performs an authenticated GET request.
func authGet(t *testing.T, ts *httptest.Server, clientID *identity.Identity, path string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", ts.URL+path, nil)
	signRequest(req, clientID)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("authGet %s: %v", path, err)
	}
	return resp
}

// authPost performs an authenticated POST request.
func authPost(t *testing.T, ts *httptest.Server, clientID *identity.Identity, path string, body []byte) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", ts.URL+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	signRequest(req, clientID)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("authPost %s: %v", path, err)
	}
	return resp
}

// authReq performs an authenticated request with arbitrary method.
func authReq(t *testing.T, ts *httptest.Server, clientID *identity.Identity, method, path string, body []byte) *http.Response {
	t.Helper()
	var req *http.Request
	if body != nil {
		req, _ = http.NewRequest(method, ts.URL+path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, _ = http.NewRequest(method, ts.URL+path, nil)
	}
	signRequest(req, clientID)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("authReq %s %s: %v", method, path, err)
	}
	return resp
}

func createTestUser(t *testing.T, ts *httptest.Server, pubKey, name string) int64 {
	t.Helper()
	body, _ := json.Marshal(map[string]string{
		"public_key":   pubKey,
		"display_name": name,
	})
	resp, err := ts.Client().Post(ts.URL+"/api/v1/users", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	defer resp.Body.Close()
	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	id, ok := result["id"].(float64)
	if !ok {
		t.Fatalf("create user response missing id: %v", result)
	}
	return int64(id)
}

func TestHealthEndpoint(t *testing.T) {
	ts, _, _ := setupAPI(t)

	resp, err := ts.Client().Get(ts.URL + "/api/v1/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}

	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	if result["status"] != "ok" {
		t.Errorf("health status: got %v, want ok", result["status"])
	}
}

func TestIdentityEndpoint(t *testing.T) {
	ts, _, _ := setupAPI(t)

	resp, err := ts.Client().Get(ts.URL + "/api/v1/identity")
	if err != nil {
		t.Fatalf("GET /identity: %v", err)
	}
	defer resp.Body.Close()

	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)

	pubKey, ok := result["public_key"].(string)
	if !ok || len(pubKey) != 64 {
		t.Errorf("public_key: got %q", pubKey)
	}
	shortID, ok := result["short_id"].(string)
	if !ok || len(shortID) != 16 {
		t.Errorf("short_id: got %q", shortID)
	}
}

func TestICEServersEndpoint(t *testing.T) {
	ts, _, _ := setupAPI(t)

	resp, err := ts.Client().Get(ts.URL + "/api/v1/ice-servers")
	if err != nil {
		t.Fatalf("GET /ice-servers: %v", err)
	}
	defer resp.Body.Close()

	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)

	servers, ok := result["ice_servers"].([]any)
	if !ok || len(servers) < 2 {
		t.Errorf("expected at least 2 ICE servers, got %d", len(servers))
	}
}

func TestUserCRUD(t *testing.T) {
	ts, _, clientID := setupAPI(t)

	// Create (public endpoint)
	id := createTestUser(t, ts, "aabbccdd11223344", "Alice")
	if id == 0 {
		t.Fatal("expected non-zero user id")
	}

	// Get (authenticated)
	resp := authGet(t, ts, clientID, fmt.Sprintf("/api/v1/users/%d", id))
	defer resp.Body.Close()
	var user map[string]any
	json.NewDecoder(resp.Body).Decode(&user)
	if user["display_name"] != "Alice" {
		t.Errorf("display_name: got %v, want Alice", user["display_name"])
	}

	// List (authenticated)
	resp2 := authGet(t, ts, clientID, "/api/v1/users")
	defer resp2.Body.Close()
	var list map[string]any
	json.NewDecoder(resp2.Body).Decode(&list)
	users := list["users"].([]any)
	if len(users) < 1 {
		t.Errorf("user count: got %d, want at least 1", len(users))
	}

	// Update (authenticated)
	updateBody, _ := json.Marshal(map[string]string{"display_name": "Alice Updated"})
	resp3 := authReq(t, ts, clientID, "PUT", fmt.Sprintf("/api/v1/users/%d", id), updateBody)
	defer resp3.Body.Close()
	if resp3.StatusCode != 200 {
		t.Errorf("update status: got %d, want 200", resp3.StatusCode)
	}

	// Delete (authenticated)
	resp4 := authReq(t, ts, clientID, "DELETE", fmt.Sprintf("/api/v1/users/%d", id), nil)
	defer resp4.Body.Close()
	if resp4.StatusCode != 200 {
		t.Errorf("delete status: got %d, want 200", resp4.StatusCode)
	}

	// Verify deleted (authenticated)
	resp5 := authGet(t, ts, clientID, fmt.Sprintf("/api/v1/users/%d", id))
	defer resp5.Body.Close()
	if resp5.StatusCode != 404 {
		t.Errorf("get deleted user status: got %d, want 404", resp5.StatusCode)
	}
}

func TestDuplicateUserKey(t *testing.T) {
	ts, _, _ := setupAPI(t)

	createTestUser(t, ts, "duplicate_key_1234", "First")

	// Second create with same key should fail
	body, _ := json.Marshal(map[string]string{
		"public_key":   "duplicate_key_1234",
		"display_name": "Second",
	})
	resp, _ := ts.Client().Post(ts.URL+"/api/v1/users", "application/json", bytes.NewReader(body))
	defer resp.Body.Close()
	if resp.StatusCode != 409 {
		t.Errorf("duplicate key status: got %d, want 409", resp.StatusCode)
	}
}

func TestDeviceRegistration(t *testing.T) {
	ts, _, clientID := setupAPI(t)

	userID := createTestUser(t, ts, "device_test_user_key", "DeviceUser")

	// Register device (authenticated)
	devBody, _ := json.Marshal(map[string]any{
		"user_id":     userID,
		"device_key":  "device_aabb_ccdd",
		"device_name": "iPhone 15",
		"platform":    "ios",
		"push_token":  "apns_token_123",
		"push_type":   "apns",
	})
	resp := authPost(t, ts, clientID, "/api/v1/devices", devBody)
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("register device status: got %d, want 201", resp.StatusCode)
	}

	// List devices (authenticated)
	resp2 := authGet(t, ts, clientID, fmt.Sprintf("/api/v1/users/%d/devices", userID))
	defer resp2.Body.Close()
	var result map[string]any
	json.NewDecoder(resp2.Body).Decode(&result)
	devices := result["devices"].([]any)
	if len(devices) != 1 {
		t.Errorf("device count: got %d, want 1", len(devices))
	}

	// Re-register same device (should upsert)
	devBody2, _ := json.Marshal(map[string]any{
		"user_id":     userID,
		"device_key":  "device_aabb_ccdd",
		"device_name": "iPhone 15 Pro",
		"platform":    "ios",
	})
	resp3 := authPost(t, ts, clientID, "/api/v1/devices", devBody2)
	defer resp3.Body.Close()

	// Should still be 1 device
	resp4 := authGet(t, ts, clientID, fmt.Sprintf("/api/v1/users/%d/devices", userID))
	defer resp4.Body.Close()
	var result2 map[string]any
	json.NewDecoder(resp4.Body).Decode(&result2)
	devices2 := result2["devices"].([]any)
	if len(devices2) != 1 {
		t.Errorf("device count after upsert: got %d, want 1", len(devices2))
	}
}

func TestMessageFlow(t *testing.T) {
	ts, _, clientID := setupAPI(t)

	senderKey := "sender_key_aabbccdd"
	recipientKey := "recipient_key_11223344"
	createTestUser(t, ts, senderKey, "Sender")
	createTestUser(t, ts, recipientKey, "Recipient")

	// Send message (authenticated)
	msgBody, _ := json.Marshal(map[string]string{
		"sender_key":    senderKey,
		"recipient_key": recipientKey,
		"content":       "encrypted_message_data",
		"content_type":  "text",
	})
	resp := authPost(t, ts, clientID, "/api/v1/messages", msgBody)
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("send message status: got %d, want 201", resp.StatusCode)
	}
	var sendResult map[string]any
	json.NewDecoder(resp.Body).Decode(&sendResult)
	msgID := int64(sendResult["id"].(float64))

	// Get pending messages (authenticated)
	resp2 := authGet(t, ts, clientID, fmt.Sprintf("/api/v1/messages/%s", recipientKey))
	defer resp2.Body.Close()
	var msgResult map[string]any
	json.NewDecoder(resp2.Body).Decode(&msgResult)
	messages := msgResult["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("pending messages: got %d, want 1", len(messages))
	}
	msg := messages[0].(map[string]any)
	if msg["sender_key"] != senderKey {
		t.Errorf("sender_key: got %v, want %s", msg["sender_key"], senderKey)
	}

	// Mark delivered (authenticated)
	resp3 := authPost(t, ts, clientID, fmt.Sprintf("/api/v1/messages/%d/delivered", msgID), nil)
	defer resp3.Body.Close()
	if resp3.StatusCode != 200 {
		t.Errorf("mark delivered status: got %d, want 200", resp3.StatusCode)
	}

	// Pending messages should be empty now
	resp4 := authGet(t, ts, clientID, fmt.Sprintf("/api/v1/messages/%s", recipientKey))
	defer resp4.Body.Close()
	var emptyResult map[string]any
	json.NewDecoder(resp4.Body).Decode(&emptyResult)
	emptyMsgs := emptyResult["messages"].([]any)
	if len(emptyMsgs) != 0 {
		t.Errorf("messages after delivery: got %d, want 0", len(emptyMsgs))
	}
}

func TestMessageToUnknownRecipient(t *testing.T) {
	ts, _, clientID := setupAPI(t)

	msgBody, _ := json.Marshal(map[string]string{
		"sender_key":    "some_sender",
		"recipient_key": "nonexistent_key",
		"content":       "hello",
	})
	resp := authPost(t, ts, clientID, "/api/v1/messages", msgBody)
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("message to unknown recipient: got %d, want 404", resp.StatusCode)
	}
}

func TestContactsCRUD(t *testing.T) {
	ts, _, clientID := setupAPI(t)

	userID := createTestUser(t, ts, "contacts_user_key", "ContactUser")

	// Add contact (authenticated)
	contactBody, _ := json.Marshal(map[string]any{
		"user_id":      userID,
		"contact_key":  "friend_key_1234",
		"display_name": "My Friend",
	})
	resp := authPost(t, ts, clientID, "/api/v1/contacts", contactBody)
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("add contact status: got %d, want 201", resp.StatusCode)
	}
	var addResult map[string]any
	json.NewDecoder(resp.Body).Decode(&addResult)
	contactID := int64(addResult["id"].(float64))

	// List contacts (authenticated)
	resp2 := authGet(t, ts, clientID, fmt.Sprintf("/api/v1/users/%d/contacts", userID))
	defer resp2.Body.Close()
	var listResult map[string]any
	json.NewDecoder(resp2.Body).Decode(&listResult)
	contacts := listResult["contacts"].([]any)
	if len(contacts) != 1 {
		t.Errorf("contact count: got %d, want 1", len(contacts))
	}

	// Block contact (authenticated)
	blockBody, _ := json.Marshal(map[string]bool{"blocked": true})
	resp3 := authReq(t, ts, clientID, "PUT", fmt.Sprintf("/api/v1/contacts/%d/block", contactID), blockBody)
	defer resp3.Body.Close()
	if resp3.StatusCode != 200 {
		t.Errorf("block contact status: got %d, want 200", resp3.StatusCode)
	}

	// Delete contact (authenticated)
	resp4 := authReq(t, ts, clientID, "DELETE", fmt.Sprintf("/api/v1/contacts/%d", contactID), nil)
	defer resp4.Body.Close()
	if resp4.StatusCode != 200 {
		t.Errorf("delete contact status: got %d, want 200", resp4.StatusCode)
	}
}

func TestCallLogEndpoint(t *testing.T) {
	ts, db, clientID := setupAPI(t)

	userID := createTestUser(t, ts, "calls_user_key", "CallUser")

	// Insert a call log entry directly
	db.Exec(
		`INSERT INTO call_log (user_id, direction, caller_key, callee_key, status, duration_sec)
		 VALUES (?, 'internal', 'caller_abc', 'callee_def', 'answered', 45)`,
		userID,
	)

	resp := authGet(t, ts, clientID, fmt.Sprintf("/api/v1/users/%d/calls", userID))
	defer resp.Body.Close()
	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	calls := result["calls"].([]any)
	if len(calls) != 1 {
		t.Errorf("call count: got %d, want 1", len(calls))
	}
	call := calls[0].(map[string]any)
	if call["status"] != "answered" {
		t.Errorf("call status: got %v, want answered", call["status"])
	}
	if int(call["duration_sec"].(float64)) != 45 {
		t.Errorf("duration: got %v, want 45", call["duration_sec"])
	}
}

func TestRoutingRulesCRUD(t *testing.T) {
	ts, _, clientID := setupAPI(t)

	userID := createTestUser(t, ts, "routing_user_key", "RoutingUser")

	// Create rule (authenticated)
	ruleBody, _ := json.Marshal(map[string]any{
		"user_id":    userID,
		"priority":   10,
		"name":       "Family direct",
		"conditions": `{"caller_group": "family"}`,
		"action":     "ring",
	})
	resp := authPost(t, ts, clientID, "/api/v1/routing", ruleBody)
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("create rule status: got %d, want 201", resp.StatusCode)
	}
	var createResult map[string]any
	json.NewDecoder(resp.Body).Decode(&createResult)
	ruleID := int64(createResult["id"].(float64))

	// List rules (authenticated)
	resp2 := authGet(t, ts, clientID, fmt.Sprintf("/api/v1/users/%d/routing", userID))
	defer resp2.Body.Close()
	var listResult map[string]any
	json.NewDecoder(resp2.Body).Decode(&listResult)
	rules := listResult["rules"].([]any)
	if len(rules) != 1 {
		t.Errorf("rule count: got %d, want 1", len(rules))
	}

	// Update rule (authenticated)
	updateBody, _ := json.Marshal(map[string]any{"name": "Updated Rule", "priority": 5})
	resp3 := authReq(t, ts, clientID, "PUT", fmt.Sprintf("/api/v1/routing/%d", ruleID), updateBody)
	defer resp3.Body.Close()
	if resp3.StatusCode != 200 {
		t.Errorf("update rule status: got %d, want 200", resp3.StatusCode)
	}

	// Delete rule (authenticated)
	resp4 := authReq(t, ts, clientID, "DELETE", fmt.Sprintf("/api/v1/routing/%d", ruleID), nil)
	defer resp4.Body.Close()
	if resp4.StatusCode != 200 {
		t.Errorf("delete rule status: got %d, want 200", resp4.StatusCode)
	}
}

func TestConfigEndpoint(t *testing.T) {
	ts, db, clientID := setupAPI(t)

	// Insert some config
	db.Exec("INSERT INTO server_config (key, value) VALUES ('turn_url', 'turn:example.com:3478')")
	db.Exec("INSERT INTO server_config (key, value) VALUES ('version', '0.1.0')")

	resp := authGet(t, ts, clientID, "/api/v1/config")
	defer resp.Body.Close()
	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	cfg := result["config"].(map[string]any)
	if cfg["turn_url"] != "turn:example.com:3478" {
		t.Errorf("turn_url: got %v", cfg["turn_url"])
	}
	if cfg["version"] != "0.1.0" {
		t.Errorf("version: got %v", cfg["version"])
	}
}

func TestICEServersWithTURN(t *testing.T) {
	ts, db, _ := setupAPI(t)

	// Configure a TURN server
	db.Exec("INSERT INTO server_config (key, value) VALUES ('turn_url', 'turn:my.turn.server:3478')")
	db.Exec("INSERT INTO server_config (key, value) VALUES ('turn_username', 'myuser')")
	db.Exec("INSERT INTO server_config (key, value) VALUES ('turn_credential', 'mysecret')")

	// ICE servers is a public endpoint
	resp, _ := ts.Client().Get(ts.URL + "/api/v1/ice-servers")
	defer resp.Body.Close()
	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)

	servers := result["ice_servers"].([]any)
	if len(servers) != 3 { // 2 STUN + 1 TURN
		t.Errorf("ICE server count: got %d, want 3", len(servers))
	}

	// Check TURN server
	turn := servers[2].(map[string]any)
	if turn["urls"] != "turn:my.turn.server:3478" {
		t.Errorf("TURN url: got %v", turn["urls"])
	}
	if turn["username"] != "myuser" {
		t.Errorf("TURN username: got %v", turn["username"])
	}
}
