package signaling

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Parsons-ai/comms-server/internal/store"
	"github.com/coder/websocket"
)

// Message types for signaling protocol.
const (
	MsgTypeOffer     = "offer"
	MsgTypeAnswer    = "answer"
	MsgTypeCandidate = "candidate"
	MsgTypeHangup    = "hangup"
	MsgTypePing      = "ping"
	MsgTypePong      = "pong"
	MsgTypeAuth      = "auth"
	MsgTypeAuthOK    = "auth_ok"
	MsgTypeError        = "error"
	MsgTypeIncomingCall  = "incoming_call"
	MsgTypeIncomingSMS   = "incoming_sms"
	MsgTypeCallAccept    = "call_accept"
	MsgTypeCallReject    = "call_reject"
	MsgTypeDirectMessage = "direct_message"
)

// Message is the wire format for signaling messages.
type Message struct {
	Type    string          `json:"type"`
	From    string          `json:"from,omitempty"`    // sender's public key hex
	To      string          `json:"to,omitempty"`      // recipient's public key hex
	Payload json.RawMessage `json:"payload,omitempty"` // SDP, ICE candidate, etc.
}

// Peer represents a connected WebSocket client.
type Peer struct {
	PublicKey string
	Conn      *websocket.Conn
	cancel    context.CancelFunc
}

// activeCalls tracks in-progress calls for duration logging.
type activeCall struct {
	callerKey string
	calleeKey string
	startedAt time.Time
	dbID      int64
}

// InternalPeerHandler receives signaling messages for internal (non-WebSocket) peers.
type InternalPeerHandler func(msg Message)

// internalPeer is a virtual peer that receives messages via callback instead of WebSocket.
type internalPeer struct {
	pubKey  string
	handler InternalPeerHandler
}

// Server is the WebSocket signaling server for WebRTC connection setup.
type Server struct {
	logger      *slog.Logger
	db          *store.DB // optional, for call logging
	mu          sync.RWMutex
	peers       map[string]*Peer // pubkey -> peer (WebSocket peers)
	internalMu  sync.RWMutex
	internalPeers map[string]*internalPeer // pubkey -> internal peer
	callMu      sync.Mutex
	activeCalls map[string]*activeCall // callKey -> activeCall
	mux         *http.ServeMux
	srv         *http.Server
	addr        string
}

// New creates a new signaling server.
// db is optional — if nil, call logging is disabled.
func New(addr string, db *store.DB, logger *slog.Logger) *Server {
	s := &Server{
		logger:        logger.With("service", "signaling"),
		db:            db,
		peers:         make(map[string]*Peer),
		internalPeers: make(map[string]*internalPeer),
		activeCalls:   make(map[string]*activeCall),
		addr:          addr,
	}

	s.mux = http.NewServeMux()
	s.mux.HandleFunc("/ws", s.handleWS)
	s.mux.HandleFunc("/health", s.handleHealth)

	s.srv = &http.Server{
		Addr:    addr,
		Handler: s.mux,
	}

	return s
}

// Name implements Service.
func (s *Server) Name() string { return "signaling" }

// Start implements Service.
func (s *Server) Start(ctx context.Context) error {
	go func() {
		s.logger.Info("signaling server listening", "addr", s.addr)
		if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Error("signaling server error", "error", err)
		}
	}()
	return nil
}

// Stop implements Service.
func (s *Server) Stop(ctx context.Context) error {
	// Close all peer connections
	s.mu.Lock()
	for _, p := range s.peers {
		p.Conn.Close(websocket.StatusGoingAway, "server shutting down")
		if p.cancel != nil {
			p.cancel()
		}
	}
	s.peers = make(map[string]*Peer)
	s.mu.Unlock()

	return s.srv.Shutdown(ctx)
}

// Health implements Service.
func (s *Server) Health() error {
	return nil
}

// PeerCount returns the number of connected peers.
func (s *Server) PeerCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.peers)
}

// IsUserOnline checks if a user (by public key) has an active WebSocket connection.
func (s *Server) IsUserOnline(pubKey string) bool {
	s.mu.RLock()
	_, ok := s.peers[pubKey]
	s.mu.RUnlock()
	return ok
}

// RegisterInternalPeer registers a virtual peer that receives messages via callback.
// This allows server-side components (like the SIP bridge) to participate in
// signaling without a WebSocket connection.
func (s *Server) RegisterInternalPeer(pubKey string, handler InternalPeerHandler) {
	s.internalMu.Lock()
	s.internalPeers[pubKey] = &internalPeer{pubKey: pubKey, handler: handler}
	s.internalMu.Unlock()
	s.logger.Info("internal peer registered", "pubkey", safeTrunc(pubKey))
}

// UnregisterInternalPeer removes a virtual peer.
func (s *Server) UnregisterInternalPeer(pubKey string) {
	s.internalMu.Lock()
	delete(s.internalPeers, pubKey)
	s.internalMu.Unlock()
	s.logger.Info("internal peer unregistered", "pubkey", safeTrunc(pubKey))
}

// SendMessage sends a signaling message from an internal peer.
// This is used by the bridge to send offers/answers/candidates to mobile peers.
func (s *Server) SendMessage(msg Message) {
	s.relay(context.Background(), msg)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok","peers":%d}`, s.PeerCount())
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"}, // TODO: restrict in production
	})
	if err != nil {
		s.logger.Error("websocket accept failed", "error", err)
		return
	}

	// Use Background context — r.Context() gets cancelled after the WebSocket
	// upgrade completes, which kills the connection immediately.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// First message must be auth
	_, data, err := conn.Read(ctx)
	if err != nil {
		conn.Close(websocket.StatusProtocolError, "read failed")
		return
	}

	var authMsg Message
	if err := json.Unmarshal(data, &authMsg); err != nil || authMsg.Type != MsgTypeAuth {
		s.sendError(ctx, conn, "first message must be auth")
		conn.Close(websocket.StatusProtocolError, "auth required")
		return
	}

	pubKey := authMsg.From
	if pubKey == "" {
		s.sendError(ctx, conn, "auth message must include 'from' field with public key")
		conn.Close(websocket.StatusProtocolError, "missing pubkey")
		return
	}

	// TODO: verify signature challenge for real auth
	// For now, trust the claimed public key

	peer := &Peer{
		PublicKey: pubKey,
		Conn:     conn,
		cancel:   cancel,
	}

	s.mu.Lock()
	// Disconnect existing connection for same pubkey (reconnect scenario)
	if existing, ok := s.peers[pubKey]; ok {
		// Cancel context first — this makes the old goroutine's Read() return an error,
		// cleanly exiting its read loop. Don't call Close() here since the old goroutine
		// is still in Read() and Close() would cause a concurrent read/close.
		if existing.cancel != nil {
			existing.cancel()
		}
	}
	s.peers[pubKey] = peer
	s.mu.Unlock()

	s.logger.Info("peer connected", "pubkey", safeTrunc(pubKey))

	// Send auth OK
	authOKData, _ := json.Marshal(Message{Type: MsgTypeAuthOK})
	if err := conn.Write(ctx, websocket.MessageText, authOKData); err != nil {
		s.logger.Warn("auth_ok write failed", "pubkey", safeTrunc(pubKey), "error", err.Error())
		return
	}

	// Read loop
	defer func() {
		s.mu.Lock()
		// Only remove if this peer is still the registered one (not replaced by reconnect)
		if current, ok := s.peers[pubKey]; ok && current == peer {
			delete(s.peers, pubKey)
		}
		s.mu.Unlock()
		s.logger.Info("peer disconnected", "pubkey", safeTrunc(pubKey))
	}()

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			s.logger.Info("peer read error", "pubkey", safeTrunc(pubKey), "error", err.Error())
			return // connection closed
		}

		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			s.sendError(ctx, conn, "invalid message format")
			continue
		}

		msg.From = pubKey // enforce sender identity

		switch msg.Type {
		case MsgTypePing:
			s.sendMsg(ctx, conn, Message{Type: MsgTypePong})
		case MsgTypeOffer, MsgTypeAnswer, MsgTypeCandidate, MsgTypeHangup,
			MsgTypeCallAccept, MsgTypeCallReject, MsgTypeDirectMessage:
			s.relay(ctx, msg)
		default:
			s.sendError(ctx, conn, fmt.Sprintf("unknown message type: %s", msg.Type))
		}
	}
}

// safeTrunc returns at most the first 16 characters of s, or s if shorter.
func safeTrunc(s string) string {
	if len(s) > 16 {
		return s[:16]
	}
	return s
}

// relay forwards a message to the intended recipient.
func (s *Server) relay(ctx context.Context, msg Message) {
	if msg.To == "" {
		s.logger.Warn("relay: missing 'to' field", "from", safeTrunc(msg.From), "type", msg.Type)
		return
	}

	// Track call state
	switch msg.Type {
	case MsgTypeOffer:
		s.trackCallStart(msg.From, msg.To)
	case MsgTypeHangup:
		s.trackCallEnd(msg.From, msg.To)
	}

	// Try WebSocket peer first
	s.mu.RLock()
	target, ok := s.peers[msg.To]
	s.mu.RUnlock()

	if ok {
		s.sendMsg(context.Background(), target.Conn, msg)
		return
	}

	// Try internal peer (bridge, etc.)
	s.internalMu.RLock()
	internal, ok := s.internalPeers[msg.To]
	s.internalMu.RUnlock()

	if ok {
		internal.handler(msg)
		return
	}

	s.logger.Debug("relay: target not connected", "to", safeTrunc(msg.To), "from", safeTrunc(msg.From))
}

func (s *Server) sendMsg(ctx context.Context, conn *websocket.Conn, msg Message) {
	data, err := json.Marshal(msg)
	if err != nil {
		s.logger.Error("marshal message", "error", err)
		return
	}
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		s.logger.Debug("write failed", "error", err)
	}
}

func (s *Server) sendError(ctx context.Context, conn *websocket.Conn, errMsg string) {
	payload, _ := json.Marshal(map[string]string{"message": errMsg})
	s.sendMsg(ctx, conn, Message{Type: MsgTypeError, Payload: payload})
}

// callKey creates a consistent key for a call pair (sorted so both directions match).
func callKey(a, b string) string {
	if a < b {
		return a + ":" + b
	}
	return b + ":" + a
}

// trackCallStart logs a new call when an offer is sent.
func (s *Server) trackCallStart(from, to string) {
	if s.db == nil {
		return
	}

	key := callKey(from, to)
	s.callMu.Lock()
	defer s.callMu.Unlock()

	// Don't duplicate if already tracking this call
	if _, exists := s.activeCalls[key]; exists {
		return
	}

	// Find user IDs for caller and callee
	var callerUserID, calleeUserID int64
	s.db.QueryRow("SELECT id FROM users WHERE public_key = ?", from).Scan(&callerUserID)
	s.db.QueryRow("SELECT id FROM users WHERE public_key = ?", to).Scan(&calleeUserID)

	// Use callee's user_id as the call log owner (inbound perspective)
	userID := calleeUserID
	if userID == 0 {
		userID = callerUserID
	}
	if userID == 0 {
		return // no known users, skip logging
	}

	result, err := s.db.Exec(
		`INSERT INTO call_log (user_id, direction, caller_key, callee_key, status, started_at)
		 VALUES (?, 'internal', ?, ?, 'answered', datetime('now'))`,
		userID, from, to,
	)
	if err != nil {
		s.logger.Warn("failed to log call start", "error", err)
		return
	}

	dbID, _ := result.LastInsertId()
	s.activeCalls[key] = &activeCall{
		callerKey: from,
		calleeKey: to,
		startedAt: time.Now(),
		dbID:      dbID,
	}
}

// trackCallEnd updates the call log when a hangup is received.
func (s *Server) trackCallEnd(from, to string) {
	if s.db == nil {
		return
	}

	key := callKey(from, to)
	s.callMu.Lock()
	call, exists := s.activeCalls[key]
	if exists {
		delete(s.activeCalls, key)
	}
	s.callMu.Unlock()

	if !exists || call.dbID == 0 {
		return
	}

	duration := int(time.Since(call.startedAt).Seconds())
	s.db.Exec(
		"UPDATE call_log SET ended_at = datetime('now'), duration_sec = ? WHERE id = ?",
		duration, call.dbID,
	)
}
