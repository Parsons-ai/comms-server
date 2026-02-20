package signaling

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestSignalingServerStartStop(t *testing.T) {
	s := New("127.0.0.1:0", nil, testLogger())
	ctx := context.Background()

	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop(ctx)

	if s.PeerCount() != 0 {
		t.Errorf("expected 0 peers, got %d", s.PeerCount())
	}
}

func setupTestServer(t *testing.T) (*httptest.Server, *Server) {
	t.Helper()
	s := New("127.0.0.1:0", nil, testLogger())
	ts := httptest.NewServer(s.mux)
	return ts, s
}

func connectPeer(t *testing.T, url, pubKey string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + url[4:] + "/ws" // http:// -> ws://
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	// Send auth
	authMsg := Message{Type: MsgTypeAuth, From: pubKey}
	data, _ := json.Marshal(authMsg)
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("write auth: %v", err)
	}

	// Read auth_ok
	_, resp, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read auth_ok: %v", err)
	}
	var msg Message
	json.Unmarshal(resp, &msg)
	if msg.Type != MsgTypeAuthOK {
		t.Fatalf("expected auth_ok, got %s", msg.Type)
	}

	return conn
}

func TestPeerConnect(t *testing.T) {
	ts, s := setupTestServer(t)
	defer ts.Close()

	conn := connectPeer(t, ts.URL, "peer1_public_key_hex_aabbccdd")
	defer conn.Close(websocket.StatusNormalClosure, "")

	// Give time for goroutine to register
	time.Sleep(50 * time.Millisecond)

	if s.PeerCount() != 1 {
		t.Errorf("expected 1 peer, got %d", s.PeerCount())
	}
}

func TestPeerRelay(t *testing.T) {
	ts, _ := setupTestServer(t)
	defer ts.Close()

	peer1Key := "peer1_aaaa_bbbb_cccc_dddd_eeee_ffff_0000"
	peer2Key := "peer2_1111_2222_3333_4444_5555_6666_7777"

	conn1 := connectPeer(t, ts.URL, peer1Key)
	defer conn1.Close(websocket.StatusNormalClosure, "")

	conn2 := connectPeer(t, ts.URL, peer2Key)
	defer conn2.Close(websocket.StatusNormalClosure, "")

	time.Sleep(50 * time.Millisecond)

	ctx := context.Background()

	// Peer1 sends an offer to Peer2
	sdpPayload, _ := json.Marshal(map[string]string{"sdp": "v=0\r\nfake SDP"})
	offerMsg := Message{
		Type:    MsgTypeOffer,
		From:    peer1Key,
		To:      peer2Key,
		Payload: sdpPayload,
	}
	data, _ := json.Marshal(offerMsg)
	if err := conn1.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("write offer: %v", err)
	}

	// Peer2 should receive the offer
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, resp, err := conn2.Read(readCtx)
	if err != nil {
		t.Fatalf("read offer: %v", err)
	}

	var received Message
	json.Unmarshal(resp, &received)
	if received.Type != MsgTypeOffer {
		t.Errorf("expected offer, got %s", received.Type)
	}
	if received.From != peer1Key {
		t.Errorf("expected from=%s, got %s", peer1Key, received.From)
	}
}

func TestPingPong(t *testing.T) {
	ts, _ := setupTestServer(t)
	defer ts.Close()

	conn := connectPeer(t, ts.URL, "ping_test_peer_key_1234567890ab")
	defer conn.Close(websocket.StatusNormalClosure, "")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Send ping
	ping := Message{Type: MsgTypePing}
	data, _ := json.Marshal(ping)
	conn.Write(ctx, websocket.MessageText, data)

	// Read pong
	_, resp, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read pong: %v", err)
	}

	var msg Message
	json.Unmarshal(resp, &msg)
	if msg.Type != MsgTypePong {
		t.Errorf("expected pong, got %s", msg.Type)
	}
}

func TestReconnect(t *testing.T) {
	ts, s := setupTestServer(t)
	defer ts.Close()

	pubKey := "reconnect_test_key_1234567890ab"

	// First connection
	conn1 := connectPeer(t, ts.URL, pubKey)
	time.Sleep(50 * time.Millisecond)

	if s.PeerCount() != 1 {
		t.Errorf("expected 1 peer after first connect, got %d", s.PeerCount())
	}

	// Second connection with same key — should replace
	conn2 := connectPeer(t, ts.URL, pubKey)
	time.Sleep(50 * time.Millisecond)

	if s.PeerCount() != 1 {
		t.Errorf("expected 1 peer after reconnect, got %d", s.PeerCount())
	}

	conn1.Close(websocket.StatusNormalClosure, "")
	conn2.Close(websocket.StatusNormalClosure, "")
}

func TestHealthEndpoint(t *testing.T) {
	ts, _ := setupTestServer(t)
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}
}
