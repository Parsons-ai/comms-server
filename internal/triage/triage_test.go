package triage

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/Parsons-ai/comms-server/internal/store"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func setupDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(":memory:", testLogger())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func createUser(t *testing.T, db *store.DB, key, name string) int64 {
	t.Helper()
	result, err := db.Exec("INSERT INTO users (public_key, display_name) VALUES (?, ?)", key, name)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	id, _ := result.LastInsertId()
	return id
}

func TestBlockedCallerRejected(t *testing.T) {
	db := setupDB(t)
	engine := New(db, testLogger())
	uid := createUser(t, db, "user1", "User")

	decision, err := engine.Evaluate(context.Background(), uid, CallerInfo{
		CallerKey: "blocked_caller",
		IsBlocked: true,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if decision.Action != "reject" {
		t.Errorf("action: got %s, want reject", decision.Action)
	}
	if decision.Confidence != 1.0 {
		t.Errorf("confidence: got %f, want 1.0", decision.Confidence)
	}
}

func TestRoutingRuleMatched(t *testing.T) {
	db := setupDB(t)
	engine := New(db, testLogger())
	uid := createUser(t, db, "user2", "User")

	// Create a routing rule: night calls go to voicemail
	db.Exec(`INSERT INTO routing_rules (user_id, priority, name, conditions, action, enabled)
		VALUES (?, 1, 'Night voicemail', '{"time_of_day":"night"}', 'voicemail', 1)`, uid)

	decision, err := engine.Evaluate(context.Background(), uid, CallerInfo{
		CallerKey: "some_caller",
		TimeOfDay: "night",
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if decision.Action != "voicemail" {
		t.Errorf("action: got %s, want voicemail", decision.Action)
	}
}

func TestRoutingRuleNotMatched(t *testing.T) {
	db := setupDB(t)
	engine := New(db, testLogger())
	uid := createUser(t, db, "user3", "User")

	// Create a routing rule: night calls go to voicemail
	db.Exec(`INSERT INTO routing_rules (user_id, priority, name, conditions, action, enabled)
		VALUES (?, 1, 'Night voicemail', '{"time_of_day":"night"}', 'voicemail', 1)`, uid)

	// Call during morning — rule should NOT match
	decision, err := engine.Evaluate(context.Background(), uid, CallerInfo{
		CallerKey: "some_caller",
		IsContact: true,
		TimeOfDay: "morning",
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if decision.Action != "ring" {
		t.Errorf("action: got %s, want ring (known contact default)", decision.Action)
	}
}

func TestKnownContactDefaultsToRing(t *testing.T) {
	db := setupDB(t)
	engine := New(db, testLogger())
	uid := createUser(t, db, "user4", "User")

	decision, err := engine.Evaluate(context.Background(), uid, CallerInfo{
		CallerKey: "friend_key",
		IsContact: true,
		TimeOfDay: "afternoon",
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if decision.Action != "ring" {
		t.Errorf("action: got %s, want ring", decision.Action)
	}
	if decision.Priority != 2 {
		t.Errorf("priority: got %d, want 2", decision.Priority)
	}
}

func TestUnknownCallerWithAIConfig(t *testing.T) {
	db := setupDB(t)
	engine := New(db, testLogger())
	uid := createUser(t, db, "user5", "User")

	// Set up AI config
	engine.SetConfig(uid, UserConfig{
		Greeting:       "Hello, who is this?",
		Personality:    "professional",
		EnabledTools:   []string{"take_message"},
		MaxDurationSec: 60,
	})

	decision, err := engine.Evaluate(context.Background(), uid, CallerInfo{
		CallerPhone: "+15551234567",
		TimeOfDay:   "afternoon",
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if decision.Action != "ai_screen" {
		t.Errorf("action: got %s, want ai_screen", decision.Action)
	}
}

func TestUnknownCallerNoConfig(t *testing.T) {
	db := setupDB(t)
	engine := New(db, testLogger())
	uid := createUser(t, db, "user6", "User")

	decision, err := engine.Evaluate(context.Background(), uid, CallerInfo{
		CallerPhone: "+15559876543",
		TimeOfDay:   "afternoon",
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if decision.Action != "ring" {
		t.Errorf("action: got %s, want ring (no AI config)", decision.Action)
	}
}

func TestLogConversation(t *testing.T) {
	db := setupDB(t)
	engine := New(db, testLogger())
	uid := createUser(t, db, "user7", "User")

	// Insert a call log entry first (for foreign key)
	result, _ := db.Exec(`INSERT INTO call_log (user_id, direction, status) VALUES (?, 'inbound', 'answered')`, uid)
	callID, _ := result.LastInsertId()

	convID, err := engine.LogConversation(uid, callID, "gpt-4", "AI: Hello\nCaller: Hi", "transferred")
	if err != nil {
		t.Fatalf("log conversation: %v", err)
	}
	if convID == 0 {
		t.Error("expected non-zero conversation id")
	}

	// Verify it was stored
	var outcome string
	db.QueryRow("SELECT outcome FROM ai_conversations WHERE id = ?", convID).Scan(&outcome)
	if outcome != "transferred" {
		t.Errorf("outcome: got %s, want transferred", outcome)
	}
}

func TestSetGetConfig(t *testing.T) {
	db := setupDB(t)
	engine := New(db, testLogger())
	uid := createUser(t, db, "user8", "User")

	// Set config
	err := engine.SetConfig(uid, UserConfig{
		Greeting:       "Custom greeting",
		Personality:    "friendly",
		EnabledTools:   []string{"transfer_call", "take_message"},
		MaxDurationSec: 90,
	})
	if err != nil {
		t.Fatalf("set config: %v", err)
	}

	// Get config
	config, err := engine.GetConfig(uid)
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if config.Greeting != "Custom greeting" {
		t.Errorf("greeting: got %q", config.Greeting)
	}
	if config.MaxDurationSec != 90 {
		t.Errorf("max_duration: got %d, want 90", config.MaxDurationSec)
	}
	if len(config.EnabledTools) != 2 {
		t.Errorf("tools count: got %d, want 2", len(config.EnabledTools))
	}

	// Update config (upsert)
	err = engine.SetConfig(uid, UserConfig{
		Greeting:       "Updated greeting",
		Personality:    "formal",
		EnabledTools:   []string{"transfer_call"},
		MaxDurationSec: 120,
	})
	if err != nil {
		t.Fatalf("update config: %v", err)
	}

	config2, _ := engine.GetConfig(uid)
	if config2.Greeting != "Updated greeting" {
		t.Errorf("updated greeting: got %q", config2.Greeting)
	}
}

func TestDisabledRuleSkipped(t *testing.T) {
	db := setupDB(t)
	engine := New(db, testLogger())
	uid := createUser(t, db, "user9", "User")

	// Create a disabled rule
	db.Exec(`INSERT INTO routing_rules (user_id, priority, name, conditions, action, enabled)
		VALUES (?, 1, 'Disabled rule', '{}', 'reject', 0)`, uid)

	decision, err := engine.Evaluate(context.Background(), uid, CallerInfo{
		CallerKey: "caller",
		IsContact: true,
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	// Disabled rule should be skipped, known contact defaults to ring
	if decision.Action != "ring" {
		t.Errorf("action: got %s, want ring", decision.Action)
	}
}

func TestTimeOfDay(t *testing.T) {
	tests := []struct {
		hour   int
		expect string
	}{
		{3, "night"},
		{7, "morning"},
		{14, "afternoon"},
		{20, "evening"},
	}
	for _, tt := range tests {
		got := TimeOfDay(time.Date(2026, 1, 1, tt.hour, 0, 0, 0, time.UTC))
		if got != tt.expect {
			t.Errorf("TimeOfDay(%d): got %s, want %s", tt.hour, got, tt.expect)
		}
	}
}

func TestRulePriorityOrder(t *testing.T) {
	db := setupDB(t)
	engine := New(db, testLogger())
	uid := createUser(t, db, "user10", "User")

	// Rule 1 (low priority number = evaluated first): reject at night
	db.Exec(`INSERT INTO routing_rules (user_id, priority, name, conditions, action, enabled)
		VALUES (?, 1, 'Night reject', '{"time_of_day":"night"}', 'reject', 1)`, uid)

	// Rule 2 (higher priority number): always ring contacts
	db.Exec(`INSERT INTO routing_rules (user_id, priority, name, conditions, action, enabled)
		VALUES (?, 10, 'Always ring contacts', '{"is_contact":true}', 'ring', 1)`, uid)

	// Night + contact: the night reject rule should match first
	decision, err := engine.Evaluate(context.Background(), uid, CallerInfo{
		CallerKey: "friend",
		IsContact: true,
		TimeOfDay: "night",
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if decision.Action != "reject" {
		t.Errorf("action: got %s, want reject (lower priority rule wins)", decision.Action)
	}
}
