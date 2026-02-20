package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/Parsons-ai/comms-server/internal/identity"
	"github.com/Parsons-ai/comms-server/internal/store"
	"github.com/Parsons-ai/comms-server/internal/triage"
)

// Server is the REST API server for the mobile app.
type Server struct {
	logger   *slog.Logger
	db       *store.DB
	identity *identity.Identity
	triage   *triage.Engine
	mux      *http.ServeMux
	srv      *http.Server
	addr     string
}

// New creates a new API server.
func New(addr string, db *store.DB, id *identity.Identity, logger *slog.Logger) *Server {
	s := &Server{
		logger:   logger.With("service", "api"),
		db:       db,
		identity: id,
		triage:   triage.New(db, logger),
		addr:     addr,
	}

	s.mux = http.NewServeMux()
	s.routes()

	s.srv = &http.Server{
		Addr:         addr,
		Handler:      s.authMiddleware(s.mux),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	return s
}

func (s *Server) routes() {
	// Health & identity
	s.mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	s.mux.HandleFunc("GET /api/v1/identity", s.handleIdentity)
	s.mux.HandleFunc("GET /api/v1/ice-servers", s.handleICEServers)

	// Users
	s.mux.HandleFunc("GET /api/v1/users", s.handleListUsers)
	s.mux.HandleFunc("POST /api/v1/users", s.handleCreateUser)
	s.mux.HandleFunc("GET /api/v1/users/{id}", s.handleGetUser)
	s.mux.HandleFunc("PUT /api/v1/users/{id}", s.handleUpdateUser)
	s.mux.HandleFunc("DELETE /api/v1/users/{id}", s.handleDeleteUser)

	// Devices
	s.mux.HandleFunc("POST /api/v1/devices", s.handleRegisterDevice)
	s.mux.HandleFunc("GET /api/v1/users/{id}/devices", s.handleListDevices)
	s.mux.HandleFunc("DELETE /api/v1/devices/{id}", s.handleDeleteDevice)

	// Messages (relay queue)
	s.mux.HandleFunc("POST /api/v1/messages", s.handleSendMessage)
	s.mux.HandleFunc("GET /api/v1/messages/{recipient_key}", s.handleGetMessages)
	s.mux.HandleFunc("POST /api/v1/messages/{id}/delivered", s.handleMarkDelivered)

	// Contacts
	s.mux.HandleFunc("GET /api/v1/users/{id}/contacts", s.handleListContacts)
	s.mux.HandleFunc("POST /api/v1/contacts", s.handleAddContact)
	s.mux.HandleFunc("DELETE /api/v1/contacts/{id}", s.handleDeleteContact)
	s.mux.HandleFunc("PUT /api/v1/contacts/{id}/block", s.handleBlockContact)

	// Call log
	s.mux.HandleFunc("GET /api/v1/users/{id}/calls", s.handleListCalls)

	// Routing rules
	s.mux.HandleFunc("GET /api/v1/users/{id}/routing", s.handleListRouting)
	s.mux.HandleFunc("POST /api/v1/routing", s.handleCreateRouting)
	s.mux.HandleFunc("PUT /api/v1/routing/{id}", s.handleUpdateRouting)
	s.mux.HandleFunc("DELETE /api/v1/routing/{id}", s.handleDeleteRouting)

	// Server config
	s.mux.HandleFunc("GET /api/v1/config", s.handleGetConfig)

	// AI Triage
	s.mux.HandleFunc("GET /api/v1/users/{id}/ai-config", s.handleGetAIConfig)
	s.mux.HandleFunc("PUT /api/v1/users/{id}/ai-config", s.handleSetAIConfig)
	s.mux.HandleFunc("POST /api/v1/triage/evaluate", s.handleTriageEvaluate)
	s.mux.HandleFunc("GET /api/v1/users/{id}/ai-conversations", s.handleListAIConversations)

	// Crypto key exchange
	s.mux.HandleFunc("GET /api/v1/users/{id}/x25519-key", s.handleGetX25519Key)
}

// Name implements Service.
func (s *Server) Name() string { return "api" }

// Start implements Service.
func (s *Server) Start(ctx context.Context) error {
	go func() {
		s.logger.Info("API server listening", "addr", s.addr)
		if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Error("API server error", "error", err)
		}
	}()
	return nil
}

// Stop implements Service.
func (s *Server) Stop(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}

// Health implements Service.
func (s *Server) Health() error {
	return s.db.Ping()
}

// Handler returns the HTTP handler (with auth middleware) for testing.
func (s *Server) Handler() http.Handler {
	return s.authMiddleware(s.mux)
}

// --- Health & Identity ---

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.json(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": "0.1.0",
	})
}

func (s *Server) handleIdentity(w http.ResponseWriter, r *http.Request) {
	s.json(w, http.StatusOK, map[string]any{
		"public_key": s.identity.PublicKeyHex(),
		"short_id":   s.identity.ShortID(),
	})
}

func (s *Server) handleICEServers(w http.ResponseWriter, r *http.Request) {
	// Default STUN servers (free, public)
	// Users can configure their own TURN server via server_config
	servers := []map[string]any{
		{"urls": "stun:stun.l.google.com:19302"},
		{"urls": "stun:stun1.l.google.com:19302"},
	}

	// Check for custom TURN server in config
	var turnURL, turnUser, turnCred string
	s.db.QueryRow("SELECT value FROM server_config WHERE key = 'turn_url'").Scan(&turnURL)
	s.db.QueryRow("SELECT value FROM server_config WHERE key = 'turn_username'").Scan(&turnUser)
	s.db.QueryRow("SELECT value FROM server_config WHERE key = 'turn_credential'").Scan(&turnCred)

	if turnURL != "" {
		turn := map[string]any{"urls": turnURL}
		if turnUser != "" {
			turn["username"] = turnUser
		}
		if turnCred != "" {
			turn["credential"] = turnCred
		}
		servers = append(servers, turn)
	}

	s.json(w, http.StatusOK, map[string]any{"ice_servers": servers})
}

// --- Users ---

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query("SELECT id, public_key, display_name, role, status, created_at, last_seen FROM users ORDER BY created_at")
	if err != nil {
		s.serverError(w, "query users", err)
		return
	}
	defer rows.Close()

	type user struct {
		ID          int64   `json:"id"`
		PublicKey   string  `json:"public_key"`
		DisplayName string  `json:"display_name"`
		Role        string  `json:"role"`
		Status      string  `json:"status"`
		CreatedAt   string  `json:"created_at"`
		LastSeen    *string `json:"last_seen"`
	}

	var users []user
	for rows.Next() {
		var u user
		if err := rows.Scan(&u.ID, &u.PublicKey, &u.DisplayName, &u.Role, &u.Status, &u.CreatedAt, &u.LastSeen); err != nil {
			s.serverError(w, "scan user", err)
			return
		}
		users = append(users, u)
	}
	if users == nil {
		users = []user{}
	}
	s.json(w, http.StatusOK, map[string]any{"users": users})
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PublicKey   string `json:"public_key"`
		X25519Key  string `json:"x25519_key"`
		DisplayName string `json:"display_name"`
		Role        string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.clientError(w, "invalid request body")
		return
	}
	if req.PublicKey == "" {
		s.clientError(w, "public_key is required")
		return
	}
	if req.Role == "" {
		req.Role = "user"
	}

	result, err := s.db.Exec(
		"INSERT INTO users (public_key, x25519_key, display_name, role) VALUES (?, ?, ?, ?)",
		req.PublicKey, nilIfEmpty(req.X25519Key), req.DisplayName, req.Role,
	)
	if err != nil {
		s.json(w, http.StatusConflict, map[string]string{"error": "create user: " + err.Error()})
		return
	}
	id, _ := result.LastInsertId()

	s.json(w, http.StatusCreated, map[string]any{
		"id":         id,
		"public_key": req.PublicKey,
	})
}

func (s *Server) handleGetUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var u struct {
		ID          int64   `json:"id"`
		PublicKey   string  `json:"public_key"`
		DisplayName string  `json:"display_name"`
		Role        string  `json:"role"`
		Status      string  `json:"status"`
		CreatedAt   string  `json:"created_at"`
		LastSeen    *string `json:"last_seen"`
	}

	err := s.db.QueryRow(
		"SELECT id, public_key, display_name, role, status, created_at, last_seen FROM users WHERE id = ?", id,
	).Scan(&u.ID, &u.PublicKey, &u.DisplayName, &u.Role, &u.Status, &u.CreatedAt, &u.LastSeen)
	if err != nil {
		s.json(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}

	s.json(w, http.StatusOK, u)
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req struct {
		DisplayName *string `json:"display_name"`
		Status      *string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.clientError(w, "invalid request body")
		return
	}

	if req.DisplayName != nil {
		if _, err := s.db.Exec("UPDATE users SET display_name = ?, updated_at = datetime('now') WHERE id = ?", *req.DisplayName, id); err != nil {
			s.serverError(w, "update user", err)
			return
		}
	}
	if req.Status != nil {
		if _, err := s.db.Exec("UPDATE users SET status = ?, updated_at = datetime('now') WHERE id = ?", *req.Status, id); err != nil {
			s.serverError(w, "update user status", err)
			return
		}
	}

	s.json(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	result, err := s.db.Exec("DELETE FROM users WHERE id = ?", id)
	if err != nil {
		s.serverError(w, "delete user", err)
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		s.json(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}
	s.json(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// --- Devices ---

func (s *Server) handleRegisterDevice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID     int64  `json:"user_id"`
		DeviceKey  string `json:"device_key"`
		DeviceName string `json:"device_name"`
		Platform   string `json:"platform"`
		PushToken  string `json:"push_token"`
		PushType   string `json:"push_type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.clientError(w, "invalid request body")
		return
	}
	if req.UserID == 0 || req.DeviceKey == "" {
		s.clientError(w, "user_id and device_key are required")
		return
	}

	result, err := s.db.Exec(
		`INSERT INTO devices (user_id, device_key, device_name, platform, push_token, push_type, last_seen)
		 VALUES (?, ?, ?, ?, ?, ?, datetime('now'))
		 ON CONFLICT(device_key) DO UPDATE SET
		   device_name = excluded.device_name,
		   push_token = excluded.push_token,
		   push_type = excluded.push_type,
		   last_seen = datetime('now')`,
		req.UserID, req.DeviceKey, req.DeviceName, req.Platform, nilIfEmpty(req.PushToken), nilIfEmpty(req.PushType),
	)
	if err != nil {
		s.json(w, http.StatusConflict, map[string]string{"error": "register device: " + err.Error()})
		return
	}
	id, _ := result.LastInsertId()

	s.json(w, http.StatusCreated, map[string]any{"id": id, "device_key": req.DeviceKey})
}

func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")

	rows, err := s.db.Query(
		"SELECT id, device_key, device_name, platform, push_token, push_type, last_seen, created_at FROM devices WHERE user_id = ? ORDER BY last_seen DESC",
		userID,
	)
	if err != nil {
		s.serverError(w, "query devices", err)
		return
	}
	defer rows.Close()

	type device struct {
		ID         int64   `json:"id"`
		DeviceKey  string  `json:"device_key"`
		DeviceName string  `json:"device_name"`
		Platform   string  `json:"platform"`
		PushToken  *string `json:"push_token"`
		PushType   *string `json:"push_type"`
		LastSeen   *string `json:"last_seen"`
		CreatedAt  string  `json:"created_at"`
	}

	var devices []device
	for rows.Next() {
		var d device
		rows.Scan(&d.ID, &d.DeviceKey, &d.DeviceName, &d.Platform, &d.PushToken, &d.PushType, &d.LastSeen, &d.CreatedAt)
		devices = append(devices, d)
	}
	if devices == nil {
		devices = []device{}
	}
	s.json(w, http.StatusOK, map[string]any{"devices": devices})
}

func (s *Server) handleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	result, err := s.db.Exec("DELETE FROM devices WHERE id = ?", id)
	if err != nil {
		s.serverError(w, "delete device", err)
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		s.json(w, http.StatusNotFound, map[string]string{"error": "device not found"})
		return
	}
	s.json(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// --- Messages (relay queue — encrypted, deleted after delivery) ---

func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SenderKey    string `json:"sender_key"`
		RecipientKey string `json:"recipient_key"`
		Content      string `json:"content"`      // base64-encoded encrypted bytes
		ContentType  string `json:"content_type"`  // text, media, system
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.clientError(w, "invalid request body")
		return
	}
	if req.SenderKey == "" || req.RecipientKey == "" || req.Content == "" {
		s.clientError(w, "sender_key, recipient_key, and content are required")
		return
	}
	if req.ContentType == "" {
		req.ContentType = "text"
	}

	// Look up recipient user ID
	var recipientID int64
	err := s.db.QueryRow("SELECT id FROM users WHERE public_key = ?", req.RecipientKey).Scan(&recipientID)
	if err != nil {
		s.json(w, http.StatusNotFound, map[string]string{"error": "recipient not found"})
		return
	}

	result, insertErr := s.db.Exec(
		`INSERT INTO messages (sender_key, recipient_id, content, content_type, size_bytes, expires_at)
		 VALUES (?, ?, ?, ?, ?, datetime('now', '+7 days'))`,
		req.SenderKey, recipientID, []byte(req.Content), req.ContentType, len(req.Content),
	)
	if insertErr != nil {
		s.serverError(w, "queue message", insertErr)
		return
	}
	id, _ := result.LastInsertId()

	s.json(w, http.StatusCreated, map[string]any{"id": id})
}

func (s *Server) handleGetMessages(w http.ResponseWriter, r *http.Request) {
	recipientKey := r.PathValue("recipient_key")

	// Look up recipient user ID
	var recipientID int64
	if err := s.db.QueryRow("SELECT id FROM users WHERE public_key = ?", recipientKey).Scan(&recipientID); err != nil {
		s.json(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}

	rows, err := s.db.Query(
		`SELECT id, sender_key, content, content_type, size_bytes, created_at
		 FROM messages WHERE recipient_id = ? AND delivered = 0
		 ORDER BY created_at`,
		recipientID,
	)
	if err != nil {
		s.serverError(w, "query messages", err)
		return
	}
	defer rows.Close()

	type msg struct {
		ID          int64  `json:"id"`
		SenderKey   string `json:"sender_key"`
		Content     string `json:"content"` // base64 of encrypted bytes
		ContentType string `json:"content_type"`
		SizeBytes   int    `json:"size_bytes"`
		CreatedAt   string `json:"created_at"`
	}

	var messages []msg
	for rows.Next() {
		var m msg
		var content []byte
		rows.Scan(&m.ID, &m.SenderKey, &content, &m.ContentType, &m.SizeBytes, &m.CreatedAt)
		m.Content = string(content)
		messages = append(messages, m)
	}
	if messages == nil {
		messages = []msg{}
	}
	s.json(w, http.StatusOK, map[string]any{"messages": messages})
}

func (s *Server) handleMarkDelivered(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	result, err := s.db.Exec("UPDATE messages SET delivered = 1 WHERE id = ? AND delivered = 0", id)
	if err != nil {
		s.serverError(w, "mark delivered", err)
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		s.json(w, http.StatusNotFound, map[string]string{"error": "message not found or already delivered"})
		return
	}
	s.json(w, http.StatusOK, map[string]string{"status": "delivered"})
}

// --- Contacts ---

func (s *Server) handleListContacts(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")

	rows, err := s.db.Query(
		`SELECT id, contact_key, display_name, phone_hash, verified, blocked, created_at
		 FROM contacts WHERE user_id = ? ORDER BY display_name`,
		userID,
	)
	if err != nil {
		s.serverError(w, "query contacts", err)
		return
	}
	defer rows.Close()

	type contact struct {
		ID          int64   `json:"id"`
		ContactKey  string  `json:"contact_key"`
		DisplayName string  `json:"display_name"`
		PhoneHash   *string `json:"phone_hash"`
		Verified    bool    `json:"verified"`
		Blocked     bool    `json:"blocked"`
		CreatedAt   string  `json:"created_at"`
	}

	var contacts []contact
	for rows.Next() {
		var c contact
		var verified, blocked int
		rows.Scan(&c.ID, &c.ContactKey, &c.DisplayName, &c.PhoneHash, &verified, &blocked, &c.CreatedAt)
		c.Verified = verified == 1
		c.Blocked = blocked == 1
		contacts = append(contacts, c)
	}
	if contacts == nil {
		contacts = []contact{}
	}
	s.json(w, http.StatusOK, map[string]any{"contacts": contacts})
}

func (s *Server) handleAddContact(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID      int64  `json:"user_id"`
		ContactKey  string `json:"contact_key"`
		DisplayName string `json:"display_name"`
		PhoneHash   string `json:"phone_hash"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.clientError(w, "invalid request body")
		return
	}
	if req.UserID == 0 || req.ContactKey == "" {
		s.clientError(w, "user_id and contact_key are required")
		return
	}

	result, err := s.db.Exec(
		"INSERT INTO contacts (user_id, contact_key, display_name, phone_hash) VALUES (?, ?, ?, ?)",
		req.UserID, req.ContactKey, req.DisplayName, nilIfEmpty(req.PhoneHash),
	)
	if err != nil {
		s.json(w, http.StatusConflict, map[string]string{"error": "add contact: " + err.Error()})
		return
	}
	id, _ := result.LastInsertId()

	s.json(w, http.StatusCreated, map[string]any{"id": id})
}

func (s *Server) handleDeleteContact(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	result, err := s.db.Exec("DELETE FROM contacts WHERE id = ?", id)
	if err != nil {
		s.serverError(w, "delete contact", err)
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		s.json(w, http.StatusNotFound, map[string]string{"error": "contact not found"})
		return
	}
	s.json(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleBlockContact(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Blocked bool `json:"blocked"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.clientError(w, "invalid request body")
		return
	}

	blocked := 0
	if req.Blocked {
		blocked = 1
	}
	result, err := s.db.Exec("UPDATE contacts SET blocked = ? WHERE id = ?", blocked, id)
	if err != nil {
		s.serverError(w, "block contact", err)
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		s.json(w, http.StatusNotFound, map[string]string{"error": "contact not found"})
		return
	}
	s.json(w, http.StatusOK, map[string]string{"status": "updated"})
}

// --- Call Log ---

func (s *Server) handleListCalls(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")

	rows, err := s.db.Query(
		`SELECT id, direction, caller_key, caller_phone, callee_key, callee_phone,
		        status, duration_sec, started_at, ended_at, ai_summary
		 FROM call_log WHERE user_id = ? ORDER BY started_at DESC LIMIT 100`,
		userID,
	)
	if err != nil {
		s.serverError(w, "query calls", err)
		return
	}
	defer rows.Close()

	type call struct {
		ID          int64   `json:"id"`
		Direction   string  `json:"direction"`
		CallerKey   *string `json:"caller_key"`
		CallerPhone *string `json:"caller_phone"`
		CalleeKey   *string `json:"callee_key"`
		CalleePhone *string `json:"callee_phone"`
		Status      string  `json:"status"`
		DurationSec int     `json:"duration_sec"`
		StartedAt   string  `json:"started_at"`
		EndedAt     *string `json:"ended_at"`
		AISummary   *string `json:"ai_summary"`
	}

	var calls []call
	for rows.Next() {
		var c call
		rows.Scan(&c.ID, &c.Direction, &c.CallerKey, &c.CallerPhone, &c.CalleeKey, &c.CalleePhone,
			&c.Status, &c.DurationSec, &c.StartedAt, &c.EndedAt, &c.AISummary)
		calls = append(calls, c)
	}
	if calls == nil {
		calls = []call{}
	}
	s.json(w, http.StatusOK, map[string]any{"calls": calls})
}

// --- Routing Rules ---

func (s *Server) handleListRouting(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")

	rows, err := s.db.Query(
		`SELECT id, priority, name, conditions, action, destination, enabled, created_at
		 FROM routing_rules WHERE user_id = ? ORDER BY priority`,
		userID,
	)
	if err != nil {
		s.serverError(w, "query routing", err)
		return
	}
	defer rows.Close()

	type rule struct {
		ID          int64  `json:"id"`
		Priority    int    `json:"priority"`
		Name        string `json:"name"`
		Conditions  string `json:"conditions"`
		Action      string `json:"action"`
		Destination string `json:"destination"`
		Enabled     bool   `json:"enabled"`
		CreatedAt   string `json:"created_at"`
	}

	var rules []rule
	for rows.Next() {
		var r rule
		var enabled int
		rows.Scan(&r.ID, &r.Priority, &r.Name, &r.Conditions, &r.Action, &r.Destination, &enabled, &r.CreatedAt)
		r.Enabled = enabled == 1
		rules = append(rules, r)
	}
	if rules == nil {
		rules = []rule{}
	}
	s.json(w, http.StatusOK, map[string]any{"rules": rules})
}

func (s *Server) handleCreateRouting(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID      int64  `json:"user_id"`
		Priority    int    `json:"priority"`
		Name        string `json:"name"`
		Conditions  string `json:"conditions"`
		Action      string `json:"action"`
		Destination string `json:"destination"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.clientError(w, "invalid request body")
		return
	}
	if req.UserID == 0 || req.Action == "" {
		s.clientError(w, "user_id and action are required")
		return
	}
	if req.Conditions == "" {
		req.Conditions = "{}"
	}

	result, err := s.db.Exec(
		`INSERT INTO routing_rules (user_id, priority, name, conditions, action, destination)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		req.UserID, req.Priority, req.Name, req.Conditions, req.Action, req.Destination,
	)
	if err != nil {
		s.json(w, http.StatusBadRequest, map[string]string{"error": "create rule: " + err.Error()})
		return
	}
	id, _ := result.LastInsertId()

	s.json(w, http.StatusCreated, map[string]any{"id": id})
}

func (s *Server) handleUpdateRouting(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req struct {
		Priority    *int    `json:"priority"`
		Name        *string `json:"name"`
		Conditions  *string `json:"conditions"`
		Action      *string `json:"action"`
		Destination *string `json:"destination"`
		Enabled     *bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.clientError(w, "invalid request body")
		return
	}

	if req.Priority != nil {
		s.db.Exec("UPDATE routing_rules SET priority = ?, updated_at = datetime('now') WHERE id = ?", *req.Priority, id)
	}
	if req.Name != nil {
		s.db.Exec("UPDATE routing_rules SET name = ?, updated_at = datetime('now') WHERE id = ?", *req.Name, id)
	}
	if req.Conditions != nil {
		s.db.Exec("UPDATE routing_rules SET conditions = ?, updated_at = datetime('now') WHERE id = ?", *req.Conditions, id)
	}
	if req.Action != nil {
		s.db.Exec("UPDATE routing_rules SET action = ?, updated_at = datetime('now') WHERE id = ?", *req.Action, id)
	}
	if req.Destination != nil {
		s.db.Exec("UPDATE routing_rules SET destination = ?, updated_at = datetime('now') WHERE id = ?", *req.Destination, id)
	}
	if req.Enabled != nil {
		enabled := 0
		if *req.Enabled {
			enabled = 1
		}
		s.db.Exec("UPDATE routing_rules SET enabled = ?, updated_at = datetime('now') WHERE id = ?", enabled, id)
	}

	s.json(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *Server) handleDeleteRouting(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	result, err := s.db.Exec("DELETE FROM routing_rules WHERE id = ?", id)
	if err != nil {
		s.serverError(w, "delete rule", err)
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		s.json(w, http.StatusNotFound, map[string]string{"error": "rule not found"})
		return
	}
	s.json(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// --- Server Config ---

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query("SELECT key, value FROM server_config ORDER BY key")
	if err != nil {
		s.serverError(w, "query config", err)
		return
	}
	defer rows.Close()

	cfg := make(map[string]string)
	for rows.Next() {
		var k, v string
		rows.Scan(&k, &v)
		cfg[k] = v
	}
	s.json(w, http.StatusOK, map[string]any{"config": cfg})
}

// --- Helpers ---

func (s *Server) json(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func (s *Server) clientError(w http.ResponseWriter, msg string) {
	s.json(w, http.StatusBadRequest, map[string]string{"error": msg})
}

func (s *Server) serverError(w http.ResponseWriter, context string, err error) {
	s.logger.Error(context, "error", err)
	s.json(w, http.StatusInternalServerError, map[string]string{"error": context})
}

// Mux returns the underlying ServeMux.
func (s *Server) Mux() *http.ServeMux {
	return s.mux
}

// Addr returns the address string.
func (s *Server) Addr() string {
	return fmt.Sprintf("http://%s", s.addr)
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// --- AI Triage ---

func (s *Server) handleGetAIConfig(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")
	var uid int64
	fmt.Sscanf(userID, "%d", &uid)

	config, err := s.triage.GetConfig(uid)
	if err != nil {
		s.json(w, http.StatusNotFound, map[string]string{"error": "no AI config found"})
		return
	}
	s.json(w, http.StatusOK, config)
}

func (s *Server) handleSetAIConfig(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")
	var uid int64
	fmt.Sscanf(userID, "%d", &uid)

	var config triage.UserConfig
	if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
		s.clientError(w, "invalid request body")
		return
	}
	if err := s.triage.SetConfig(uid, config); err != nil {
		s.serverError(w, "set AI config", err)
		return
	}
	s.json(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *Server) handleTriageEvaluate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID int64              `json:"user_id"`
		Caller triage.CallerInfo  `json:"caller"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.clientError(w, "invalid request body")
		return
	}

	decision, err := s.triage.Evaluate(r.Context(), req.UserID, req.Caller)
	if err != nil {
		s.serverError(w, "triage evaluate", err)
		return
	}
	s.json(w, http.StatusOK, decision)
}

func (s *Server) handleListAIConversations(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")

	rows, err := s.db.Query(
		`SELECT id, call_id, model, started_at, ended_at, outcome
		 FROM ai_conversations WHERE user_id = ? ORDER BY started_at DESC LIMIT 50`,
		userID,
	)
	if err != nil {
		s.serverError(w, "query AI conversations", err)
		return
	}
	defer rows.Close()

	type conv struct {
		ID        int64   `json:"id"`
		CallID    *int64  `json:"call_id"`
		Model     string  `json:"model"`
		StartedAt string  `json:"started_at"`
		EndedAt   *string `json:"ended_at"`
		Outcome   *string `json:"outcome"`
	}

	var convs []conv
	for rows.Next() {
		var c conv
		rows.Scan(&c.ID, &c.CallID, &c.Model, &c.StartedAt, &c.EndedAt, &c.Outcome)
		convs = append(convs, c)
	}
	if convs == nil {
		convs = []conv{}
	}
	s.json(w, http.StatusOK, map[string]any{"conversations": convs})
}

// --- Crypto Key Exchange ---

func (s *Server) handleGetX25519Key(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")

	var x25519Key *string
	err := s.db.QueryRow("SELECT x25519_key FROM users WHERE id = ?", userID).Scan(&x25519Key)
	if err != nil || x25519Key == nil {
		s.json(w, http.StatusNotFound, map[string]string{"error": "X25519 key not found"})
		return
	}
	s.json(w, http.StatusOK, map[string]string{"x25519_public_key": *x25519Key})
}
