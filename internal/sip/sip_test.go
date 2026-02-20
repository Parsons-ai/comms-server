package sip

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/Parsons-ai/comms-server/internal/store"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func setupBridge(t *testing.T) (*httptest.Server, *Bridge, *store.DB) {
	t.Helper()
	db, err := store.Open(":memory:", testLogger())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	cfg := Config{
		Provider: ProviderTelnyx,
		APIKey:   "test_api_key",
		Domain:   "sip.test.com",
	}
	bridge := New("127.0.0.1:0", db, cfg, testLogger())
	ts := httptest.NewServer(bridge.Handler())
	t.Cleanup(func() {
		ts.Close()
		db.Close()
	})
	return ts, bridge, db
}

func TestSIPHealth(t *testing.T) {
	ts, _, _ := setupBridge(t)

	resp, err := ts.Client().Get(ts.URL + "/sip/health")
	if err != nil {
		t.Fatalf("GET /sip/health: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}

	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	if result["provider"] != "telnyx" {
		t.Errorf("provider: got %v, want telnyx", result["provider"])
	}
}

func TestIncomingCallWebhook(t *testing.T) {
	ts, bridge, _ := setupBridge(t)

	var receivedCall *ActiveCall
	bridge.SetIncomingCallHandler(func(call *ActiveCall) {
		receivedCall = call
	})

	// Simulate Telnyx call.initiated webhook
	event := TelnyxEvent{}
	event.Data.EventType = "call.initiated"
	event.Data.ID = "evt_123"
	event.Data.Payload.CallControlID = "call_ctrl_abc"
	event.Data.Payload.From = "+15551234567"
	event.Data.Payload.To = "+15559876543"
	event.Data.Payload.Direction = "incoming"

	body, _ := json.Marshal(event)
	resp, err := ts.Client().Post(ts.URL+"/sip/webhook", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /sip/webhook: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("webhook status: got %d, want 200", resp.StatusCode)
	}

	if bridge.ActiveCallCount() != 1 {
		t.Errorf("active calls: got %d, want 1", bridge.ActiveCallCount())
	}

	if receivedCall == nil {
		t.Fatal("incoming call handler not called")
	}
	if receivedCall.CallerPhone != "+15551234567" {
		t.Errorf("caller: got %s, want +15551234567", receivedCall.CallerPhone)
	}
	if receivedCall.Direction != CallInbound {
		t.Errorf("direction: got %s, want inbound", receivedCall.Direction)
	}
}

func TestCallHangupWebhook(t *testing.T) {
	ts, bridge, _ := setupBridge(t)

	// First, initiate a call
	initEvent := TelnyxEvent{}
	initEvent.Data.EventType = "call.initiated"
	initEvent.Data.ID = "evt_init"
	initEvent.Data.Payload.CallControlID = "call_ctrl_xyz"
	initEvent.Data.Payload.From = "+15551111111"
	initEvent.Data.Payload.To = "+15552222222"
	initEvent.Data.Payload.Direction = "incoming"

	body, _ := json.Marshal(initEvent)
	ts.Client().Post(ts.URL+"/sip/webhook", "application/json", bytes.NewReader(body))

	if bridge.ActiveCallCount() != 1 {
		t.Fatalf("active calls after init: got %d, want 1", bridge.ActiveCallCount())
	}

	// Now hangup
	hangupEvent := TelnyxEvent{}
	hangupEvent.Data.EventType = "call.hangup"
	hangupEvent.Data.ID = "evt_hangup"
	hangupEvent.Data.Payload.CallControlID = "call_ctrl_xyz"
	hangupEvent.Data.Payload.HangupCause = "normal_clearing"

	body2, _ := json.Marshal(hangupEvent)
	ts.Client().Post(ts.URL+"/sip/webhook", "application/json", bytes.NewReader(body2))

	if bridge.ActiveCallCount() != 0 {
		t.Errorf("active calls after hangup: got %d, want 0", bridge.ActiveCallCount())
	}
}

func TestListActiveCalls(t *testing.T) {
	ts, _, _ := setupBridge(t)

	// No calls
	resp, _ := ts.Client().Get(ts.URL + "/sip/calls")
	defer resp.Body.Close()
	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	calls := result["calls"].([]any)
	if len(calls) != 0 {
		t.Errorf("initial calls: got %d, want 0", len(calls))
	}

	// Add a call via webhook
	event := TelnyxEvent{}
	event.Data.EventType = "call.initiated"
	event.Data.Payload.CallControlID = "call_list_test"
	event.Data.Payload.From = "+15553333333"
	event.Data.Payload.To = "+15554444444"
	event.Data.Payload.Direction = "outgoing"
	body, _ := json.Marshal(event)
	ts.Client().Post(ts.URL+"/sip/webhook", "application/json", bytes.NewReader(body))

	// List again
	resp2, _ := ts.Client().Get(ts.URL + "/sip/calls")
	defer resp2.Body.Close()
	var result2 map[string]any
	json.NewDecoder(resp2.Body).Decode(&result2)
	calls2 := result2["calls"].([]any)
	if len(calls2) != 1 {
		t.Errorf("calls after init: got %d, want 1", len(calls2))
	}
}

func TestCallWithKnownUser(t *testing.T) {
	ts, bridge, db := setupBridge(t)

	// Create a user with a DID
	result, _ := db.Exec("INSERT INTO users (public_key, display_name) VALUES (?, ?)", "user_sip_key_abc", "SIP User")
	userID, _ := result.LastInsertId()
	db.Exec("INSERT INTO sip_trunks (provider, domain) VALUES ('telnyx', 'sip.test.com')")
	db.Exec("INSERT INTO dids (trunk_id, number, user_id) VALUES (1, '+15559876543', ?)", userID)

	var receivedCall *ActiveCall
	bridge.SetIncomingCallHandler(func(call *ActiveCall) {
		receivedCall = call
	})

	// Incoming call to the user's DID
	event := TelnyxEvent{}
	event.Data.EventType = "call.initiated"
	event.Data.Payload.CallControlID = "call_known_user"
	event.Data.Payload.From = "+15551234567"
	event.Data.Payload.To = "+15559876543"
	event.Data.Payload.Direction = "incoming"
	body, _ := json.Marshal(event)
	resp, _ := ts.Client().Post(ts.URL+"/sip/webhook", "application/json", bytes.NewReader(body))
	resp.Body.Close()

	if receivedCall == nil {
		t.Fatal("incoming call handler not called")
	}
	if receivedCall.CalleeKey != "user_sip_key_abc" {
		t.Errorf("callee key: got %q, want user_sip_key_abc", receivedCall.CalleeKey)
	}

	// Verify call was logged
	var callCount int
	db.QueryRow("SELECT count(*) FROM call_log WHERE user_id = ?", userID).Scan(&callCount)
	if callCount != 1 {
		t.Errorf("call_log entries: got %d, want 1", callCount)
	}
}

func TestInvalidWebhookPayload(t *testing.T) {
	ts, _, _ := setupBridge(t)

	resp, _ := ts.Client().Post(ts.URL+"/sip/webhook", "application/json", bytes.NewReader([]byte("not json")))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid payload status: got %d, want 400", resp.StatusCode)
	}
}
