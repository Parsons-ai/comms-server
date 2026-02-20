package store

import (
	"log/slog"
	"os"
	"testing"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestOpenInMemory(t *testing.T) {
	db, err := Open(":memory:", testLogger())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Verify tables exist
	tables := []string{
		"users", "devices", "messages", "contacts",
		"routing_rules", "call_log", "sip_trunks", "dids",
		"ai_conversations", "ai_config", "server_config",
		"storage_usage", "schema_version",
	}

	for _, table := range tables {
		var name string
		err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found: %v", table, err)
		}
	}
}

func TestSchemaVersion(t *testing.T) {
	db, err := Open(":memory:", testLogger())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	var version int
	if err := db.QueryRow("SELECT version FROM schema_version ORDER BY version DESC LIMIT 1").Scan(&version); err != nil {
		t.Fatalf("query version: %v", err)
	}
	if version != 1 {
		t.Errorf("schema version: got %d, want 1", version)
	}
}

func TestUserCRUD(t *testing.T) {
	db, err := Open(":memory:", testLogger())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Create
	result, err := db.Exec(
		"INSERT INTO users (public_key, display_name, role) VALUES (?, ?, ?)",
		"abc123def456", "Test User", "admin",
	)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	userID, _ := result.LastInsertId()

	// Read
	var name, role string
	err = db.QueryRow("SELECT display_name, role FROM users WHERE id = ?", userID).Scan(&name, &role)
	if err != nil {
		t.Fatalf("select user: %v", err)
	}
	if name != "Test User" {
		t.Errorf("display_name: got %q, want %q", name, "Test User")
	}
	if role != "admin" {
		t.Errorf("role: got %q, want %q", role, "admin")
	}

	// Update
	_, err = db.Exec("UPDATE users SET display_name = ? WHERE id = ?", "Updated Name", userID)
	if err != nil {
		t.Fatalf("update user: %v", err)
	}

	// Delete
	_, err = db.Exec("DELETE FROM users WHERE id = ?", userID)
	if err != nil {
		t.Fatalf("delete user: %v", err)
	}
}

func TestDeviceForeignKey(t *testing.T) {
	db, err := Open(":memory:", testLogger())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Create user first
	result, err := db.Exec(
		"INSERT INTO users (public_key, display_name) VALUES (?, ?)",
		"user1pubkey", "User 1",
	)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	userID, _ := result.LastInsertId()

	// Create device
	_, err = db.Exec(
		"INSERT INTO devices (user_id, device_key, device_name, platform) VALUES (?, ?, ?, ?)",
		userID, "devicekey123", "iPhone 15", "ios",
	)
	if err != nil {
		t.Fatalf("insert device: %v", err)
	}

	// Try invalid foreign key
	_, err = db.Exec(
		"INSERT INTO devices (user_id, device_key, device_name) VALUES (?, ?, ?)",
		99999, "devicekey999", "Ghost Device",
	)
	if err == nil {
		t.Error("expected foreign key error for invalid user_id")
	}

	// Cascade delete — delete user should delete devices
	_, err = db.Exec("DELETE FROM users WHERE id = ?", userID)
	if err != nil {
		t.Fatalf("delete user: %v", err)
	}
	var count int
	db.QueryRow("SELECT count(*) FROM devices WHERE user_id = ?", userID).Scan(&count)
	if count != 0 {
		t.Errorf("devices not cascade deleted: got %d, want 0", count)
	}
}

func TestMessageQueue(t *testing.T) {
	db, err := Open(":memory:", testLogger())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Create user
	result, _ := db.Exec("INSERT INTO users (public_key, display_name) VALUES (?, ?)", "recvkey", "Receiver")
	recipientID, _ := result.LastInsertId()

	// Queue message
	_, err = db.Exec(
		"INSERT INTO messages (sender_key, recipient_id, content, content_type, size_bytes) VALUES (?, ?, ?, ?, ?)",
		"senderkey", recipientID, []byte("encrypted hello"), "text", 15,
	)
	if err != nil {
		t.Fatalf("insert message: %v", err)
	}

	// Query pending messages
	var msgCount int
	db.QueryRow("SELECT count(*) FROM messages WHERE recipient_id = ? AND delivered = 0", recipientID).Scan(&msgCount)
	if msgCount != 1 {
		t.Errorf("pending messages: got %d, want 1", msgCount)
	}

	// Mark delivered
	_, err = db.Exec("UPDATE messages SET delivered = 1 WHERE recipient_id = ?", recipientID)
	if err != nil {
		t.Fatalf("mark delivered: %v", err)
	}

	db.QueryRow("SELECT count(*) FROM messages WHERE recipient_id = ? AND delivered = 0", recipientID).Scan(&msgCount)
	if msgCount != 0 {
		t.Errorf("pending after delivery: got %d, want 0", msgCount)
	}
}

func TestRoutingRules(t *testing.T) {
	db, err := Open(":memory:", testLogger())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	result, _ := db.Exec("INSERT INTO users (public_key, display_name) VALUES (?, ?)", "ruleuser", "Rule User")
	userID, _ := result.LastInsertId()

	// Create routing rule
	_, err = db.Exec(
		`INSERT INTO routing_rules (user_id, priority, name, conditions, action, destination)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		userID, 10, "Family direct", `{"caller_in_group": "family"}`, "ring", "",
	)
	if err != nil {
		t.Fatalf("insert rule: %v", err)
	}

	_, err = db.Exec(
		`INSERT INTO routing_rules (user_id, priority, name, conditions, action, destination)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		userID, 20, "Unknown → AI", `{"caller": "unknown"}`, "ai_screen", "",
	)
	if err != nil {
		t.Fatalf("insert rule: %v", err)
	}

	// Query rules in priority order
	rows, err := db.Query("SELECT name, action FROM routing_rules WHERE user_id = ? ORDER BY priority", userID)
	if err != nil {
		t.Fatalf("query rules: %v", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name, action string
		rows.Scan(&name, &action)
		names = append(names, name)
	}
	if len(names) != 2 || names[0] != "Family direct" || names[1] != "Unknown → AI" {
		t.Errorf("routing rules order: got %v", names)
	}
}

func TestRoleConstraint(t *testing.T) {
	db, err := Open(":memory:", testLogger())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Valid roles
	for _, role := range []string{"admin", "user", "managed"} {
		_, err := db.Exec("INSERT INTO users (public_key, display_name, role) VALUES (?, ?, ?)",
			"key_"+role, "Test "+role, role)
		if err != nil {
			t.Errorf("valid role %q rejected: %v", role, err)
		}
	}

	// Invalid role
	_, err = db.Exec("INSERT INTO users (public_key, display_name, role) VALUES (?, ?, ?)",
		"key_invalid", "Test Invalid", "superadmin")
	if err == nil {
		t.Error("invalid role 'superadmin' accepted")
	}
}
