package store

import (
	"database/sql"
	"fmt"
	"log/slog"

	_ "modernc.org/sqlite"
)

// DB wraps the SQLite database connection.
type DB struct {
	*sql.DB
	logger *slog.Logger
}

// Open creates or opens the SQLite database at the given path.
// Use ":memory:" for in-memory testing.
func Open(path string, logger *slog.Logger) (*DB, error) {
	dsn := path
	if path != ":memory:" {
		dsn = path + "?_busy_timeout=5000"
	}

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// SQLite single-writer: limit to 1 write connection, allow multiple readers
	sqlDB.SetMaxOpenConns(1)

	// Enable foreign keys and WAL mode via PRAGMA (modernc.org/sqlite DSN doesn't support these)
	pragmas := []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
	}
	for _, p := range pragmas {
		if _, err := sqlDB.Exec(p); err != nil {
			sqlDB.Close()
			return nil, fmt.Errorf("set pragma %q: %w", p, err)
		}
	}

	db := &DB{DB: sqlDB, logger: logger}

	if err := db.migrate(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	logger.Info("database opened", "path", path)
	return db, nil
}

// migrate applies the schema. Uses IF NOT EXISTS for idempotency.
func (db *DB) migrate() error {
	_, err := db.Exec(schema)
	return err
}

// schema is the full SQLite schema for the COMMS server.
// Based on the decided 14-table normalized relational schema.
const schema = `
-- ============================================================
-- COMMS Server Schema
-- 14 tables across 6 domains
-- ============================================================

-- Domain 1: Users & Devices
CREATE TABLE IF NOT EXISTS users (
    id          INTEGER PRIMARY KEY,
    public_key  TEXT NOT NULL UNIQUE,  -- hex-encoded Ed25519 public key
    display_name TEXT NOT NULL DEFAULT '',
    role        TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('admin', 'user', 'managed')),
    status      TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended', 'inactive')),
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now')),
    last_seen   TEXT
);

CREATE TABLE IF NOT EXISTS devices (
    id          INTEGER PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    device_key  TEXT NOT NULL UNIQUE,  -- hex-encoded device-specific public key
    device_name TEXT NOT NULL DEFAULT '',
    platform    TEXT NOT NULL DEFAULT '' CHECK (platform IN ('', 'ios', 'android', 'web', 'desktop')),
    push_token  TEXT,                   -- APNs/FCM token for push notifications
    push_type   TEXT CHECK (push_type IN (NULL, 'apns', 'fcm', 'unified_push')),
    last_seen   TEXT,
    created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_devices_user_id ON devices(user_id);

-- Domain 2: Messages (relay queue — deleted after delivery)
CREATE TABLE IF NOT EXISTS messages (
    id           INTEGER PRIMARY KEY,
    sender_key   TEXT NOT NULL,          -- sender's public key
    recipient_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    device_id    INTEGER REFERENCES devices(id) ON DELETE SET NULL,
    content      BLOB NOT NULL,          -- encrypted message envelope
    content_type TEXT NOT NULL DEFAULT 'text' CHECK (content_type IN ('text', 'media', 'system')),
    size_bytes   INTEGER NOT NULL DEFAULT 0,
    created_at   TEXT NOT NULL DEFAULT (datetime('now')),
    expires_at   TEXT,                   -- auto-delete after this time
    delivered    INTEGER NOT NULL DEFAULT 0  -- 0=pending, 1=delivered
);
CREATE INDEX IF NOT EXISTS idx_messages_recipient ON messages(recipient_id, delivered);
CREATE INDEX IF NOT EXISTS idx_messages_expires ON messages(expires_at) WHERE expires_at IS NOT NULL;

-- Domain 3: Contacts & Discovery
CREATE TABLE IF NOT EXISTS contacts (
    id          INTEGER PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    contact_key TEXT NOT NULL,            -- the contact's public key
    display_name TEXT NOT NULL DEFAULT '',
    phone_hash  TEXT,                     -- SHA-256 hash of phone number (for discovery)
    verified    INTEGER NOT NULL DEFAULT 0,
    blocked     INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(user_id, contact_key)
);
CREATE INDEX IF NOT EXISTS idx_contacts_user ON contacts(user_id);
CREATE INDEX IF NOT EXISTS idx_contacts_phone_hash ON contacts(phone_hash) WHERE phone_hash IS NOT NULL;

-- Domain 4: Call Routing
CREATE TABLE IF NOT EXISTS routing_rules (
    id          INTEGER PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    priority    INTEGER NOT NULL DEFAULT 0,  -- lower = evaluated first
    name        TEXT NOT NULL DEFAULT '',
    conditions  TEXT NOT NULL DEFAULT '{}',  -- JSON: time, caller, day_of_week, etc.
    action      TEXT NOT NULL CHECK (action IN ('ring', 'ai_screen', 'voicemail', 'reject', 'forward')),
    destination TEXT,                         -- target for forward: user_id, sip:number, ai:agent_id
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_routing_user ON routing_rules(user_id, priority);

CREATE TABLE IF NOT EXISTS call_log (
    id           INTEGER PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    direction    TEXT NOT NULL CHECK (direction IN ('inbound', 'outbound', 'internal')),
    caller_key   TEXT,                   -- COMMS caller public key (if known)
    caller_phone TEXT,                   -- SIP caller phone number (if from PSTN)
    callee_key   TEXT,
    callee_phone TEXT,
    status       TEXT NOT NULL CHECK (status IN ('answered', 'missed', 'rejected', 'voicemail', 'failed')),
    duration_sec INTEGER NOT NULL DEFAULT 0,
    started_at   TEXT NOT NULL DEFAULT (datetime('now')),
    ended_at     TEXT,
    ai_summary   TEXT                    -- AI triage summary if AI handled the call
);
CREATE INDEX IF NOT EXISTS idx_calllog_user ON call_log(user_id, started_at);

-- Domain 5: SIP / VoIP
CREATE TABLE IF NOT EXISTS sip_trunks (
    id          INTEGER PRIMARY KEY,
    provider    TEXT NOT NULL,            -- telnyx, voipms, signalwire, generic
    domain      TEXT NOT NULL,
    username    TEXT,
    auth_user   TEXT,                     -- may differ from username
    status      TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled', 'error')),
    config      TEXT NOT NULL DEFAULT '{}', -- JSON: provider-specific settings
    created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS dids (
    id          INTEGER PRIMARY KEY,
    trunk_id    INTEGER NOT NULL REFERENCES sip_trunks(id) ON DELETE CASCADE,
    number      TEXT NOT NULL UNIQUE,     -- E.164 format (+15551234567)
    label       TEXT NOT NULL DEFAULT '', -- "Mom's line", "Business", etc.
    user_id     INTEGER REFERENCES users(id) ON DELETE SET NULL, -- which user this DID routes to
    created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Domain 6: AI Triage
CREATE TABLE IF NOT EXISTS ai_conversations (
    id          INTEGER PRIMARY KEY,
    call_id     INTEGER REFERENCES call_log(id) ON DELETE SET NULL,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    model       TEXT NOT NULL DEFAULT '',
    started_at  TEXT NOT NULL DEFAULT (datetime('now')),
    ended_at    TEXT,
    transcript  TEXT,                     -- full conversation transcript
    outcome     TEXT CHECK (outcome IN (NULL, 'transferred', 'voicemail', 'rejected', 'resolved'))
);
CREATE INDEX IF NOT EXISTS idx_ai_conv_user ON ai_conversations(user_id, started_at);

CREATE TABLE IF NOT EXISTS ai_config (
    id          INTEGER PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    greeting    TEXT NOT NULL DEFAULT 'Hi, you''ve reached an AI assistant. How can I help?',
    personality TEXT NOT NULL DEFAULT '',  -- custom personality prompt
    tools       TEXT NOT NULL DEFAULT '["transfer_call","take_message","check_calendar"]', -- JSON array of enabled tools
    max_duration_sec INTEGER NOT NULL DEFAULT 120,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(user_id)
);

-- Domain: Server Config
CREATE TABLE IF NOT EXISTS server_config (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- Domain: Storage tracking
CREATE TABLE IF NOT EXISTS storage_usage (
    id          INTEGER PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    category    TEXT NOT NULL CHECK (category IN ('messages', 'media', 'voicemail', 'other')),
    size_bytes  INTEGER NOT NULL DEFAULT 0,
    updated_at  TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(user_id, category)
);

-- Schema version tracking
CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL DEFAULT (datetime('now'))
);
INSERT OR IGNORE INTO schema_version (version) VALUES (1);
`
