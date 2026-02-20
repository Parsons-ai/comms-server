package sip

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Parsons-ai/comms-server/internal/store"
)

// Provider identifies the VoIP provider.
type Provider string

const (
	ProviderTelnyx    Provider = "telnyx"
	ProviderVoIPms    Provider = "voipms"
	ProviderGeneric   Provider = "generic"
)

// Config holds SIP bridge configuration.
type Config struct {
	Provider   Provider
	APIKey     string // Telnyx API key
	Domain     string // SIP domain
	Username   string
	Password   string
	WebhookURL string // URL for incoming call webhooks
}

// CallDirection indicates the direction of a call.
type CallDirection string

const (
	CallInbound  CallDirection = "inbound"
	CallOutbound CallDirection = "outbound"
)

// ActiveCall tracks an in-progress SIP call.
type ActiveCall struct {
	ID          string
	Direction   CallDirection
	CallerPhone string
	CalleePhone string
	CallerKey   string // COMMS user public key (if known)
	CalleeKey   string // COMMS user public key (if known)
	StartedAt   time.Time
	ProviderID  string // Telnyx call control ID
}

// Bridge is the SIP-to-WebRTC bridge service.
type Bridge struct {
	logger *slog.Logger
	db     *store.DB
	cfg    Config
	mux    *http.ServeMux
	srv    *http.Server
	addr   string

	mu          sync.RWMutex
	activeCalls map[string]*ActiveCall // providerID -> call

	// onIncomingCall is called when a new inbound call arrives.
	// The handler should determine routing (ring user, AI triage, voicemail, etc.)
	onIncomingCall func(call *ActiveCall)
}

// New creates a new SIP bridge.
func New(addr string, db *store.DB, cfg Config, logger *slog.Logger) *Bridge {
	b := &Bridge{
		logger:      logger.With("service", "sip"),
		db:          db,
		cfg:         cfg,
		activeCalls: make(map[string]*ActiveCall),
		addr:        addr,
	}

	b.mux = http.NewServeMux()
	b.mux.HandleFunc("POST /sip/webhook", b.handleWebhook)
	b.mux.HandleFunc("GET /sip/health", b.handleHealth)
	b.mux.HandleFunc("GET /sip/calls", b.handleListActiveCalls)

	b.srv = &http.Server{
		Addr:         addr,
		Handler:      b.mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	return b
}

// Name implements Service.
func (b *Bridge) Name() string { return "sip" }

// Start implements Service.
func (b *Bridge) Start(ctx context.Context) error {
	go func() {
		b.logger.Info("SIP bridge listening", "addr", b.addr, "provider", b.cfg.Provider)
		if err := b.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			b.logger.Error("SIP bridge error", "error", err)
		}
	}()
	return nil
}

// Stop implements Service.
func (b *Bridge) Stop(ctx context.Context) error {
	return b.srv.Shutdown(ctx)
}

// Health implements Service.
func (b *Bridge) Health() error {
	return nil
}

// SetIncomingCallHandler sets the handler for incoming calls.
func (b *Bridge) SetIncomingCallHandler(handler func(call *ActiveCall)) {
	b.onIncomingCall = handler
}

// Handler returns the HTTP handler for testing.
func (b *Bridge) Handler() http.Handler {
	return b.mux
}

// --- Webhook handling (Telnyx format) ---

// TelnyxEvent represents a Telnyx webhook event.
type TelnyxEvent struct {
	Data struct {
		EventType string `json:"event_type"`
		ID        string `json:"id"`
		Payload   struct {
			CallControlID  string `json:"call_control_id"`
			CallLegID      string `json:"call_leg_id"`
			CallSessionID  string `json:"call_session_id"`
			ConnectionID   string `json:"connection_id"`
			From           string `json:"from"`
			To             string `json:"to"`
			Direction      string `json:"direction"`
			State          string `json:"state"`
			ClientState    string `json:"client_state"`
			HangupCause    string `json:"hangup_cause"`
			HangupSource   string `json:"hangup_source"`
			StartTime      string `json:"start_time"`
			EndTime        string `json:"end_time"`
		} `json:"payload"`
	} `json:"data"`
}

func (b *Bridge) handleWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB max
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	var event TelnyxEvent
	if err := json.Unmarshal(body, &event); err != nil {
		b.logger.Warn("invalid webhook payload", "error", err)
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	b.logger.Info("webhook received",
		"event_type", event.Data.EventType,
		"from", event.Data.Payload.From,
		"to", event.Data.Payload.To,
	)

	switch event.Data.EventType {
	case "call.initiated":
		b.handleCallInitiated(event)
	case "call.answered":
		b.handleCallAnswered(event)
	case "call.hangup":
		b.handleCallHangup(event)
	}

	w.WriteHeader(http.StatusOK)
}

func (b *Bridge) handleCallInitiated(event TelnyxEvent) {
	p := event.Data.Payload

	call := &ActiveCall{
		ID:          event.Data.ID,
		CallerPhone: p.From,
		CalleePhone: p.To,
		StartedAt:   time.Now(),
		ProviderID:  p.CallControlID,
	}

	if p.Direction == "incoming" {
		call.Direction = CallInbound
	} else {
		call.Direction = CallOutbound
	}

	// Try to resolve COMMS user from DID number
	if call.Direction == CallInbound {
		var userKey string
		err := b.db.QueryRow(
			`SELECT u.public_key FROM users u
			 JOIN dids d ON d.user_id = u.id
			 WHERE d.number = ?`, call.CalleePhone,
		).Scan(&userKey)
		if err == nil {
			call.CalleeKey = userKey
		}
	}

	b.mu.Lock()
	b.activeCalls[p.CallControlID] = call
	b.mu.Unlock()

	// Log to call_log
	if call.CalleeKey != "" {
		var userID int64
		b.db.QueryRow("SELECT id FROM users WHERE public_key = ?", call.CalleeKey).Scan(&userID)
		if userID > 0 {
			b.db.Exec(
				`INSERT INTO call_log (user_id, direction, caller_phone, callee_key, status, started_at)
				 VALUES (?, ?, ?, ?, 'answered', datetime('now'))`,
				userID, string(call.Direction), call.CallerPhone, call.CalleeKey,
			)
		}
	}

	// Notify incoming call handler
	if call.Direction == CallInbound && b.onIncomingCall != nil {
		b.onIncomingCall(call)
	}
}

func (b *Bridge) handleCallAnswered(event TelnyxEvent) {
	b.mu.RLock()
	call, exists := b.activeCalls[event.Data.Payload.CallControlID]
	b.mu.RUnlock()

	if exists {
		b.logger.Info("call answered",
			"from", call.CallerPhone,
			"to", call.CalleePhone,
		)
	}
}

func (b *Bridge) handleCallHangup(event TelnyxEvent) {
	p := event.Data.Payload

	b.mu.Lock()
	call, exists := b.activeCalls[p.CallControlID]
	if exists {
		delete(b.activeCalls, p.CallControlID)
	}
	b.mu.Unlock()

	if !exists {
		return
	}

	duration := int(time.Since(call.StartedAt).Seconds())
	b.logger.Info("call ended",
		"from", call.CallerPhone,
		"to", call.CalleePhone,
		"duration", duration,
		"cause", p.HangupCause,
	)

	// Update call log
	if call.CalleeKey != "" {
		b.db.Exec(
			`UPDATE call_log SET ended_at = datetime('now'), duration_sec = ?
			 WHERE callee_key = ? AND caller_phone = ? AND ended_at IS NULL`,
			duration, call.CalleeKey, call.CallerPhone,
		)
	}
}

// --- Call control (outbound) ---

// Originate starts an outbound call via the SIP provider.
func (b *Bridge) Originate(ctx context.Context, from, to string) (string, error) {
	switch b.cfg.Provider {
	case ProviderTelnyx:
		return b.telnyxOriginate(ctx, from, to)
	default:
		return "", fmt.Errorf("provider %s not supported for originate", b.cfg.Provider)
	}
}

func (b *Bridge) telnyxOriginate(ctx context.Context, from, to string) (string, error) {
	payload := map[string]any{
		"connection_id": b.cfg.Username,
		"to":            to,
		"from":          from,
		"webhook_url":   b.cfg.WebhookURL,
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.telnyx.com/v2/calls", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+b.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("telnyx API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("telnyx API status %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Data struct {
			CallControlID string `json:"call_control_id"`
		} `json:"data"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	return result.Data.CallControlID, nil
}

// Hangup ends an active call.
func (b *Bridge) Hangup(ctx context.Context, callControlID string) error {
	switch b.cfg.Provider {
	case ProviderTelnyx:
		return b.telnyxHangup(ctx, callControlID)
	default:
		return fmt.Errorf("provider %s not supported for hangup", b.cfg.Provider)
	}
}

func (b *Bridge) telnyxHangup(ctx context.Context, callControlID string) error {
	url := fmt.Sprintf("https://api.telnyx.com/v2/calls/%s/actions/hangup", callControlID)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader([]byte("{}")))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+b.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("telnyx API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("telnyx hangup status %d", resp.StatusCode)
	}
	return nil
}

// ActiveCallCount returns the number of active calls.
func (b *Bridge) ActiveCallCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.activeCalls)
}

// --- HTTP handlers ---

func (b *Bridge) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok","provider":"%s","active_calls":%d}`,
		b.cfg.Provider, b.ActiveCallCount())
}

func (b *Bridge) handleListActiveCalls(w http.ResponseWriter, r *http.Request) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	type callInfo struct {
		ID          string `json:"id"`
		Direction   string `json:"direction"`
		CallerPhone string `json:"caller_phone"`
		CalleePhone string `json:"callee_phone"`
		Duration    int    `json:"duration_sec"`
	}

	var calls []callInfo
	for _, c := range b.activeCalls {
		calls = append(calls, callInfo{
			ID:          c.ID,
			Direction:   string(c.Direction),
			CallerPhone: c.CallerPhone,
			CalleePhone: c.CalleePhone,
			Duration:    int(time.Since(c.StartedAt).Seconds()),
		})
	}
	if calls == nil {
		calls = []callInfo{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"calls": calls})
}
