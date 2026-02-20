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

func setupAPI(t *testing.T) (*httptest.Server, *store.DB) {
	t.Helper()
	db, err := store.Open(":memory:", testLogger())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	id, _ := identity.Generate()
	srv := New("127.0.0.1:0", db, id, testLogger())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		db.Close()
	})
	return ts, db
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
	ts, _ := setupAPI(t)

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
	ts, _ := setupAPI(t)

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
	ts, _ := setupAPI(t)

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
	ts, _ := setupAPI(t)

	// Create
	id := createTestUser(t, ts, "aabbccdd11223344", "Alice")
	if id == 0 {
		t.Fatal("expected non-zero user id")
	}

	// Get
	resp, _ := ts.Client().Get(fmt.Sprintf("%s/api/v1/users/%d", ts.URL, id))
	defer resp.Body.Close()
	var user map[string]any
	json.NewDecoder(resp.Body).Decode(&user)
	if user["display_name"] != "Alice" {
		t.Errorf("display_name: got %v, want Alice", user["display_name"])
	}

	// List
	resp2, _ := ts.Client().Get(ts.URL + "/api/v1/users")
	defer resp2.Body.Close()
	var list map[string]any
	json.NewDecoder(resp2.Body).Decode(&list)
	users := list["users"].([]any)
	if len(users) != 1 {
		t.Errorf("user count: got %d, want 1", len(users))
	}

	// Update
	updateBody, _ := json.Marshal(map[string]string{"display_name": "Alice Updated"})
	req, _ := http.NewRequest("PUT", fmt.Sprintf("%s/api/v1/users/%d", ts.URL, id), bytes.NewReader(updateBody))
	req.Header.Set("Content-Type", "application/json")
	resp3, _ := ts.Client().Do(req)
	defer resp3.Body.Close()
	if resp3.StatusCode != 200 {
		t.Errorf("update status: got %d, want 200", resp3.StatusCode)
	}

	// Delete
	delReq, _ := http.NewRequest("DELETE", fmt.Sprintf("%s/api/v1/users/%d", ts.URL, id), nil)
	resp4, _ := ts.Client().Do(delReq)
	defer resp4.Body.Close()
	if resp4.StatusCode != 200 {
		t.Errorf("delete status: got %d, want 200", resp4.StatusCode)
	}

	// Verify deleted
	resp5, _ := ts.Client().Get(fmt.Sprintf("%s/api/v1/users/%d", ts.URL, id))
	defer resp5.Body.Close()
	if resp5.StatusCode != 404 {
		t.Errorf("get deleted user status: got %d, want 404", resp5.StatusCode)
	}
}

func TestDuplicateUserKey(t *testing.T) {
	ts, _ := setupAPI(t)

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
	ts, _ := setupAPI(t)

	userID := createTestUser(t, ts, "device_test_user_key", "DeviceUser")

	// Register device
	devBody, _ := json.Marshal(map[string]any{
		"user_id":     userID,
		"device_key":  "device_aabb_ccdd",
		"device_name": "iPhone 15",
		"platform":    "ios",
		"push_token":  "apns_token_123",
		"push_type":   "apns",
	})
	resp, _ := ts.Client().Post(ts.URL+"/api/v1/devices", "application/json", bytes.NewReader(devBody))
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("register device status: got %d, want 201", resp.StatusCode)
	}

	// List devices
	resp2, _ := ts.Client().Get(fmt.Sprintf("%s/api/v1/users/%d/devices", ts.URL, userID))
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
	resp3, _ := ts.Client().Post(ts.URL+"/api/v1/devices", "application/json", bytes.NewReader(devBody2))
	defer resp3.Body.Close()

	// Should still be 1 device
	resp4, _ := ts.Client().Get(fmt.Sprintf("%s/api/v1/users/%d/devices", ts.URL, userID))
	defer resp4.Body.Close()
	var result2 map[string]any
	json.NewDecoder(resp4.Body).Decode(&result2)
	devices2 := result2["devices"].([]any)
	if len(devices2) != 1 {
		t.Errorf("device count after upsert: got %d, want 1", len(devices2))
	}
}

func TestMessageFlow(t *testing.T) {
	ts, _ := setupAPI(t)

	senderKey := "sender_key_aabbccdd"
	recipientKey := "recipient_key_11223344"
	createTestUser(t, ts, senderKey, "Sender")
	createTestUser(t, ts, recipientKey, "Recipient")

	// Send message
	msgBody, _ := json.Marshal(map[string]string{
		"sender_key":    senderKey,
		"recipient_key": recipientKey,
		"content":       "encrypted_message_data",
		"content_type":  "text",
	})
	resp, _ := ts.Client().Post(ts.URL+"/api/v1/messages", "application/json", bytes.NewReader(msgBody))
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("send message status: got %d, want 201", resp.StatusCode)
	}
	var sendResult map[string]any
	json.NewDecoder(resp.Body).Decode(&sendResult)
	msgID := int64(sendResult["id"].(float64))

	// Get pending messages for recipient
	resp2, _ := ts.Client().Get(fmt.Sprintf("%s/api/v1/messages/%s", ts.URL, recipientKey))
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

	// Mark delivered
	req, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/v1/messages/%d/delivered", ts.URL, msgID), nil)
	resp3, _ := ts.Client().Do(req)
	defer resp3.Body.Close()
	if resp3.StatusCode != 200 {
		t.Errorf("mark delivered status: got %d, want 200", resp3.StatusCode)
	}

	// Pending messages should be empty now
	resp4, _ := ts.Client().Get(fmt.Sprintf("%s/api/v1/messages/%s", ts.URL, recipientKey))
	defer resp4.Body.Close()
	var emptyResult map[string]any
	json.NewDecoder(resp4.Body).Decode(&emptyResult)
	emptyMsgs := emptyResult["messages"].([]any)
	if len(emptyMsgs) != 0 {
		t.Errorf("messages after delivery: got %d, want 0", len(emptyMsgs))
	}
}

func TestMessageToUnknownRecipient(t *testing.T) {
	ts, _ := setupAPI(t)

	msgBody, _ := json.Marshal(map[string]string{
		"sender_key":    "some_sender",
		"recipient_key": "nonexistent_key",
		"content":       "hello",
	})
	resp, _ := ts.Client().Post(ts.URL+"/api/v1/messages", "application/json", bytes.NewReader(msgBody))
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("message to unknown recipient: got %d, want 404", resp.StatusCode)
	}
}

func TestContactsCRUD(t *testing.T) {
	ts, _ := setupAPI(t)

	userID := createTestUser(t, ts, "contacts_user_key", "ContactUser")

	// Add contact
	contactBody, _ := json.Marshal(map[string]any{
		"user_id":      userID,
		"contact_key":  "friend_key_1234",
		"display_name": "My Friend",
	})
	resp, _ := ts.Client().Post(ts.URL+"/api/v1/contacts", "application/json", bytes.NewReader(contactBody))
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("add contact status: got %d, want 201", resp.StatusCode)
	}
	var addResult map[string]any
	json.NewDecoder(resp.Body).Decode(&addResult)
	contactID := int64(addResult["id"].(float64))

	// List contacts
	resp2, _ := ts.Client().Get(fmt.Sprintf("%s/api/v1/users/%d/contacts", ts.URL, userID))
	defer resp2.Body.Close()
	var listResult map[string]any
	json.NewDecoder(resp2.Body).Decode(&listResult)
	contacts := listResult["contacts"].([]any)
	if len(contacts) != 1 {
		t.Errorf("contact count: got %d, want 1", len(contacts))
	}

	// Block contact
	blockBody, _ := json.Marshal(map[string]bool{"blocked": true})
	blockReq, _ := http.NewRequest("PUT", fmt.Sprintf("%s/api/v1/contacts/%d/block", ts.URL, contactID), bytes.NewReader(blockBody))
	blockReq.Header.Set("Content-Type", "application/json")
	resp3, _ := ts.Client().Do(blockReq)
	defer resp3.Body.Close()
	if resp3.StatusCode != 200 {
		t.Errorf("block contact status: got %d, want 200", resp3.StatusCode)
	}

	// Delete contact
	delReq, _ := http.NewRequest("DELETE", fmt.Sprintf("%s/api/v1/contacts/%d", ts.URL, contactID), nil)
	resp4, _ := ts.Client().Do(delReq)
	defer resp4.Body.Close()
	if resp4.StatusCode != 200 {
		t.Errorf("delete contact status: got %d, want 200", resp4.StatusCode)
	}
}

func TestCallLogEndpoint(t *testing.T) {
	ts, db := setupAPI(t)

	userID := createTestUser(t, ts, "calls_user_key", "CallUser")

	// Insert a call log entry directly
	db.Exec(
		`INSERT INTO call_log (user_id, direction, caller_key, callee_key, status, duration_sec)
		 VALUES (?, 'internal', 'caller_abc', 'callee_def', 'answered', 45)`,
		userID,
	)

	resp, _ := ts.Client().Get(fmt.Sprintf("%s/api/v1/users/%d/calls", ts.URL, userID))
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
	ts, _ := setupAPI(t)

	userID := createTestUser(t, ts, "routing_user_key", "RoutingUser")

	// Create rule
	ruleBody, _ := json.Marshal(map[string]any{
		"user_id":    userID,
		"priority":   10,
		"name":       "Family direct",
		"conditions": `{"caller_group": "family"}`,
		"action":     "ring",
	})
	resp, _ := ts.Client().Post(ts.URL+"/api/v1/routing", "application/json", bytes.NewReader(ruleBody))
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("create rule status: got %d, want 201", resp.StatusCode)
	}
	var createResult map[string]any
	json.NewDecoder(resp.Body).Decode(&createResult)
	ruleID := int64(createResult["id"].(float64))

	// List rules
	resp2, _ := ts.Client().Get(fmt.Sprintf("%s/api/v1/users/%d/routing", ts.URL, userID))
	defer resp2.Body.Close()
	var listResult map[string]any
	json.NewDecoder(resp2.Body).Decode(&listResult)
	rules := listResult["rules"].([]any)
	if len(rules) != 1 {
		t.Errorf("rule count: got %d, want 1", len(rules))
	}

	// Update rule
	updateBody, _ := json.Marshal(map[string]any{"name": "Updated Rule", "priority": 5})
	updateReq, _ := http.NewRequest("PUT", fmt.Sprintf("%s/api/v1/routing/%d", ts.URL, ruleID), bytes.NewReader(updateBody))
	updateReq.Header.Set("Content-Type", "application/json")
	resp3, _ := ts.Client().Do(updateReq)
	defer resp3.Body.Close()
	if resp3.StatusCode != 200 {
		t.Errorf("update rule status: got %d, want 200", resp3.StatusCode)
	}

	// Delete rule
	delReq, _ := http.NewRequest("DELETE", fmt.Sprintf("%s/api/v1/routing/%d", ts.URL, ruleID), nil)
	resp4, _ := ts.Client().Do(delReq)
	defer resp4.Body.Close()
	if resp4.StatusCode != 200 {
		t.Errorf("delete rule status: got %d, want 200", resp4.StatusCode)
	}
}

func TestConfigEndpoint(t *testing.T) {
	ts, db := setupAPI(t)

	// Insert some config
	db.Exec("INSERT INTO server_config (key, value) VALUES ('turn_url', 'turn:example.com:3478')")
	db.Exec("INSERT INTO server_config (key, value) VALUES ('version', '0.1.0')")

	resp, _ := ts.Client().Get(ts.URL + "/api/v1/config")
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
	ts, db := setupAPI(t)

	// Configure a TURN server
	db.Exec("INSERT INTO server_config (key, value) VALUES ('turn_url', 'turn:my.turn.server:3478')")
	db.Exec("INSERT INTO server_config (key, value) VALUES ('turn_username', 'myuser')")
	db.Exec("INSERT INTO server_config (key, value) VALUES ('turn_credential', 'mysecret')")

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
