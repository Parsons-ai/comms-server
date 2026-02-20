package cleanup

import (
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

func TestCleanExpiredMessages(t *testing.T) {
	db := setupDB(t)
	svc := New(db, time.Hour, testLogger())

	uid := createUser(t, db, "testkey1", "Test")

	// Insert an expired message
	db.Exec(`INSERT INTO messages (sender_key, recipient_id, content, size_bytes, expires_at)
		VALUES ('sender', ?, 'data', 4, datetime('now', '-1 hour'))`, uid)

	// Insert a non-expired message
	db.Exec(`INSERT INTO messages (sender_key, recipient_id, content, size_bytes, expires_at)
		VALUES ('sender', ?, 'data2', 5, datetime('now', '+1 hour'))`, uid)

	svc.cleanExpiredMessages()

	var count int
	db.QueryRow("SELECT COUNT(*) FROM messages").Scan(&count)
	if count != 1 {
		t.Errorf("messages after cleanup: got %d, want 1", count)
	}
}

func TestCleanDeliveredMessages(t *testing.T) {
	db := setupDB(t)
	svc := New(db, time.Hour, testLogger())

	uid := createUser(t, db, "testkey2", "Test")

	// Insert a delivered message from 2 hours ago
	db.Exec(`INSERT INTO messages (sender_key, recipient_id, content, size_bytes, delivered, created_at)
		VALUES ('sender', ?, 'data', 4, 1, datetime('now', '-2 hours'))`, uid)

	// Insert a delivered message from 10 minutes ago (should not be cleaned)
	db.Exec(`INSERT INTO messages (sender_key, recipient_id, content, size_bytes, delivered, created_at)
		VALUES ('sender', ?, 'data2', 5, 1, datetime('now', '-10 minutes'))`, uid)

	// Insert an undelivered message
	db.Exec(`INSERT INTO messages (sender_key, recipient_id, content, size_bytes, delivered)
		VALUES ('sender', ?, 'data3', 5, 0)`, uid)

	svc.cleanDeliveredMessages()

	var count int
	db.QueryRow("SELECT COUNT(*) FROM messages").Scan(&count)
	if count != 2 {
		t.Errorf("messages after cleanup: got %d, want 2", count)
	}
}

func TestPruneOldCallLogs(t *testing.T) {
	db := setupDB(t)
	svc := New(db, time.Hour, testLogger())

	uid := createUser(t, db, "testkey3", "Test")

	// Insert an old call log (100 days ago)
	db.Exec(`INSERT INTO call_log (user_id, direction, status, started_at)
		VALUES (?, 'inbound', 'answered', datetime('now', '-100 days'))`, uid)

	// Insert a recent call log
	db.Exec(`INSERT INTO call_log (user_id, direction, status, started_at)
		VALUES (?, 'inbound', 'answered', datetime('now', '-1 day'))`, uid)

	svc.pruneOldCallLogs()

	var count int
	db.QueryRow("SELECT COUNT(*) FROM call_log").Scan(&count)
	if count != 1 {
		t.Errorf("call logs after prune: got %d, want 1", count)
	}
}

func TestUpdateStorageUsage(t *testing.T) {
	db := setupDB(t)
	svc := New(db, time.Hour, testLogger())

	uid := createUser(t, db, "testkey4", "Test")

	// Insert some pending messages
	db.Exec(`INSERT INTO messages (sender_key, recipient_id, content, size_bytes, delivered)
		VALUES ('sender', ?, 'data', 100, 0)`, uid)
	db.Exec(`INSERT INTO messages (sender_key, recipient_id, content, size_bytes, delivered)
		VALUES ('sender', ?, 'data2', 200, 0)`, uid)

	svc.updateStorageUsage()

	var sizeBytes int
	err := db.QueryRow("SELECT size_bytes FROM storage_usage WHERE user_id = ? AND category = 'messages'", uid).Scan(&sizeBytes)
	if err != nil {
		t.Fatalf("query storage: %v", err)
	}
	if sizeBytes != 300 {
		t.Errorf("storage usage: got %d, want 300", sizeBytes)
	}

	// Run again to verify upsert works
	db.Exec(`INSERT INTO messages (sender_key, recipient_id, content, size_bytes, delivered)
		VALUES ('sender', ?, 'data3', 50, 0)`, uid)
	svc.updateStorageUsage()

	db.QueryRow("SELECT size_bytes FROM storage_usage WHERE user_id = ? AND category = 'messages'", uid).Scan(&sizeBytes)
	if sizeBytes != 350 {
		t.Errorf("storage usage after update: got %d, want 350", sizeBytes)
	}
}

func TestRunOnce(t *testing.T) {
	db := setupDB(t)
	svc := New(db, time.Hour, testLogger())

	uid := createUser(t, db, "testkey5", "Test")

	// Insert an expired message
	db.Exec(`INSERT INTO messages (sender_key, recipient_id, content, size_bytes, expires_at)
		VALUES ('sender', ?, 'expired', 7, datetime('now', '-1 hour'))`, uid)

	// RunOnce should clean it
	svc.RunOnce()

	var count int
	db.QueryRow("SELECT COUNT(*) FROM messages").Scan(&count)
	if count != 0 {
		t.Errorf("messages after RunOnce: got %d, want 0", count)
	}
}
