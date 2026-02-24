package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/Parsons-ai/comms-server/internal/identity"
	"github.com/Parsons-ai/comms-server/internal/signaling"
	"github.com/coder/websocket"
)

// incomingCallPayload mirrors bridge.IncomingCallPayload (avoids import cycle).
type incomingCallPayload struct {
	CallID       string `json:"call_id"`
	BridgeKey    string `json:"bridge_key"`
	CallerNumber string `json:"caller_number"`
	CalledDID    string `json:"called_did"`
}

// cmdStatus prints server health and capabilities.
func cmdStatus(server string) error {
	var health map[string]any
	if err := getPublic(server, "/api/v1/health", &health); err != nil {
		return fmt.Errorf("cannot reach server at %s: %w", server, err)
	}

	var caps map[string]any
	getPublic(server, "/api/v1/capabilities", &caps) //nolint:errcheck

	fmt.Printf("Server:   %s\n", server)
	fmt.Printf("Status:   %v\n", health["status"])
	fmt.Printf("Version:  %v\n", health["version"])
	if wsPort, ok := health["ws_port"]; ok {
		fmt.Printf("WS Port:  %v\n", wsPort)
	}
	if caps != nil {
		fmt.Println()
		fmt.Println("Capabilities:")
		// Print in a sensible order
		keys := []string{
			"p2p_calls", "messaging", "contacts",
			"pstn_outbound", "pstn_inbound", "ai_triage",
			"ws_port", "did_count", "sip_mode", "sip_registration",
		}
		for _, k := range keys {
			if v, ok := caps[k]; ok {
				fmt.Printf("  %-22s %v\n", k, v)
			}
		}
	}
	return nil
}

// cmdRegister generates an Ed25519 identity, registers it with the server,
// and saves the profile to ~/.comms/cli-profiles/<name>/.
func cmdRegister(server, name, displayName string) error {
	// Refuse to overwrite an existing profile
	idFile := filepath.Join(profilesDir(), name, "identity.json")
	if _, err := os.Stat(idFile); err == nil {
		return fmt.Errorf("profile %q already exists — delete %s/%s/ to re-register", name, profilesDir(), name)
	}

	// Generate keypair
	id, err := identity.Generate()
	if err != nil {
		return fmt.Errorf("generate identity: %w", err)
	}

	// Register with server (POST /api/v1/users is public — no auth required)
	result, status, err := postPublic(server, "/api/v1/users", map[string]string{
		"public_key":   id.PublicKeyHex(),
		"display_name": displayName,
		"role":         "user",
	})
	if err != nil {
		return fmt.Errorf("register with server: %w", err)
	}
	if status != 201 {
		return fmt.Errorf("registration rejected (HTTP %d): %v", status, result)
	}
	idVal, ok := result["id"].(float64)
	if !ok {
		return fmt.Errorf("unexpected server response: %v", result)
	}
	userID := int64(idVal)

	// Discover WebSocket port from capabilities (public endpoint)
	wsPort := defaultWSPort
	var caps map[string]any
	if getPublic(server, "/api/v1/capabilities", &caps) == nil {
		if wp, ok := caps["ws_port"].(float64); ok && wp > 0 {
			wsPort = int(wp)
		}
	}

	// Persist to disk
	if err := SaveProfile(name, server, wsPort, userID, id); err != nil {
		return fmt.Errorf("save profile: %w", err)
	}

	fmt.Printf("Registered: %s (%s)\n", name, displayName)
	fmt.Printf("User ID:    %d\n", userID)
	fmt.Printf("Public Key: %s\n", id.PublicKeyHex())
	fmt.Printf("Profile:    %s/%s/\n", profilesDir(), name)
	return nil
}

// cmdListen connects to the signaling WebSocket and prints every incoming message.
// Stays alive until SIGINT/SIGTERM (Ctrl+C).
func cmdListen(server, profileName string) error {
	c, err := LoadProfile(profileName, server)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	fmt.Printf("Connecting to %s ...\n", c.wsURL())
	conn, err := c.Connect(ctx)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	fmt.Printf("Connected  ✓  profile=%s  pubkey=%.16s...\n", profileName, c.identity.PublicKeyHex())
	fmt.Println("Listening for messages (Ctrl+C to quit)")
	fmt.Println(strings.Repeat("─", 60))

	listenErr := c.Listen(ctx, conn, func(msg signaling.Message) {
		ts := time.Now().Format("15:04:05")
		// Pretty-print payload JSON
		payload := ""
		if msg.Payload != nil {
			var p any
			if json.Unmarshal(msg.Payload, &p) == nil {
				if b, err := json.Marshal(p); err == nil {
					payload = string(b)
				}
			} else {
				payload = string(msg.Payload)
			}
		}
		from := msg.From
		if len(from) > 16 {
			from = from[:16] + "..."
		}
		fmt.Printf("[%s] %-22s  from=%s\n         payload=%s\n",
			ts, msg.Type, from, payload)
	})
	if ctx.Err() != nil {
		fmt.Println("\nDisconnected.")
		return nil
	}
	return listenErr
}

// ─── Simple info commands ─────────────────────────────────────────────────────

// cmdWhoami shows the profile's identity and registration info.
func cmdWhoami(server, profileName string) error {
	c, err := LoadProfile(profileName, server)
	if err != nil {
		return err
	}

	fmt.Printf("Profile:    %s\n", profileName)
	fmt.Printf("Public Key: %s\n", c.identity.PublicKeyHex())
	fmt.Printf("Server:     %s\n", c.server)
	fmt.Printf("WS Port:    %d\n", c.wsPort)

	if c.userID == 0 {
		fmt.Println("(no user ID — try re-registering)")
		return nil
	}
	fmt.Printf("User ID:    %d\n", c.userID)

	// Fetch user info from server
	var user map[string]any
	resp, err := c.get(fmt.Sprintf("/api/v1/users/%d", c.userID), &user)
	if err != nil {
		fmt.Printf("(could not fetch user info: %v)\n", err)
	} else if resp.StatusCode == 200 {
		fmt.Printf("Display:    %v\n", user["display_name"])
		fmt.Printf("Role:       %v\n", user["role"])
		fmt.Printf("Status:     %v\n", user["status"])
		if ls, ok := user["last_seen"]; ok && ls != nil {
			fmt.Printf("Last Seen:  %v\n", ls)
		}
	}

	// Fetch DIDs
	var didsResult map[string]any
	if r, err := c.get("/api/v1/dids", &didsResult); err == nil && r.StatusCode == 200 {
		if dids, ok := didsResult["dids"].([]any); ok {
			myDIDs := 0
			for _, d := range dids {
				dm, _ := d.(map[string]any)
				uid, _ := dm["user_id"].(float64)
				if int64(uid) == c.userID {
					myDIDs++
					if myDIDs == 1 {
						fmt.Println("\nDIDs:")
					}
					label := ""
					if l, ok := dm["label"]; ok && l != nil {
						label = fmt.Sprintf(" (%v)", l)
					}
					fmt.Printf("  %v%s\n", dm["number"], label)
				}
			}
			if myDIDs == 0 {
				fmt.Println("\nDIDs:       (none assigned)")
			}
		}
	}
	return nil
}

// cmdContacts lists contacts for the current user.
func cmdContacts(server, profileName string) error {
	c, err := LoadProfile(profileName, server)
	if err != nil {
		return err
	}
	if c.userID == 0 {
		return fmt.Errorf("no user ID in profile — try re-registering")
	}

	var result map[string]any
	resp, err := c.get(fmt.Sprintf("/api/v1/users/%d/contacts", c.userID), &result)
	if err != nil {
		return fmt.Errorf("get contacts: %w", err)
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("request failed (HTTP %d): %v", resp.StatusCode, result)
	}

	contacts, _ := result["contacts"].([]any)
	if len(contacts) == 0 {
		fmt.Println("No contacts yet.")
		fmt.Println("Contacts are discovered organically via calls and messages.")
		return nil
	}

	fmt.Printf("%-30s  %-8s  %-8s  %s\n", "DISPLAY NAME", "VERIFIED", "BLOCKED", "ADDED")
	fmt.Println(strings.Repeat("─", 72))
	for _, ct := range contacts {
		m, _ := ct.(map[string]any)
		name := fmt.Sprintf("%v", m["display_name"])
		if len(name) > 30 {
			name = name[:29] + "…"
		}
		verified := "no"
		if v, _ := m["verified"].(bool); v {
			verified = "yes"
		}
		blocked := "no"
		if v, _ := m["blocked"].(bool); v {
			blocked = "YES"
		}
		fmt.Printf("%-30s  %-8s  %-8s  %v\n", name, verified, blocked, m["created_at"])
	}
	return nil
}

// cmdCalls lists recent call log entries for the current user.
func cmdCalls(server, profileName string) error {
	c, err := LoadProfile(profileName, server)
	if err != nil {
		return err
	}
	if c.userID == 0 {
		return fmt.Errorf("no user ID in profile — try re-registering")
	}

	var result map[string]any
	resp, err := c.get(fmt.Sprintf("/api/v1/users/%d/calls", c.userID), &result)
	if err != nil {
		return fmt.Errorf("get calls: %w", err)
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("request failed (HTTP %d): %v", resp.StatusCode, result)
	}

	calls, _ := result["calls"].([]any)
	if len(calls) == 0 {
		fmt.Println("No call history.")
		return nil
	}

	fmt.Printf("%-5s  %-8s  %-16s  %-8s  %-8s  %s\n", "ID", "DIR", "NUMBER", "STATUS", "DURATION", "STARTED")
	fmt.Println(strings.Repeat("─", 76))
	for _, cl := range calls {
		m, _ := cl.(map[string]any)
		dir := fmt.Sprintf("%v", m["direction"])
		number := ""
		if m["direction"] == "outbound" {
			if v, ok := m["callee_phone"]; ok && v != nil {
				number = fmt.Sprintf("%v", v)
			}
		} else {
			if v, ok := m["caller_phone"]; ok && v != nil {
				number = fmt.Sprintf("%v", v)
			}
		}
		if number == "" {
			number = "(internal)"
		}
		dur := fmt.Sprintf("%vs", m["duration_sec"])
		startedAt := fmt.Sprintf("%v", m["started_at"])
		if len(startedAt) > 19 {
			startedAt = startedAt[:19]
		}
		fmt.Printf("%-5v  %-8s  %-16s  %-8s  %-8s  %s\n",
			m["id"], dir, number, m["status"], dur, startedAt)
	}
	return nil
}

// cmdDIDs lists all DIDs on the server, highlighting those owned by the user.
func cmdDIDs(server, profileName string) error {
	c, err := LoadProfile(profileName, server)
	if err != nil {
		return err
	}

	var result map[string]any
	resp, err := c.get("/api/v1/dids", &result)
	if err != nil {
		return fmt.Errorf("get DIDs: %w", err)
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("request failed (HTTP %d): %v", resp.StatusCode, result)
	}

	dids, _ := result["dids"].([]any)
	if len(dids) == 0 {
		fmt.Println("No DIDs configured on server.")
		return nil
	}

	fmt.Printf("%-16s  %-8s  %-12s  %s\n", "NUMBER", "MINE", "PROVIDER", "LABEL")
	fmt.Println(strings.Repeat("─", 56))
	for _, d := range dids {
		m, _ := d.(map[string]any)
		mine := ""
		if uid, ok := m["user_id"].(float64); ok && int64(uid) == c.userID {
			mine = "✓"
		}
		provider := ""
		if v, ok := m["provider"]; ok && v != nil {
			provider = fmt.Sprintf("%v", v)
		}
		label := ""
		if v, ok := m["label"]; ok && v != nil {
			label = fmt.Sprintf("%v", v)
		}
		fmt.Printf("%-16v  %-8s  %-12s  %s\n", m["number"], mine, provider, label)
	}
	return nil
}

// ─── DM commands ──────────────────────────────────────────────────────────────

// cmdDMSend sends a COMMS native direct message to another user by public key.
func cmdDMSend(server, profileName, toPubKey, text string) error {
	c, err := LoadProfile(profileName, server)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	conn, err := c.Connect(ctx)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	payload, _ := json.Marshal(map[string]string{"text": text})
	msg := signaling.Message{
		Type:    signaling.MsgTypeDirectMessage,
		From:    c.identity.PublicKeyHex(),
		To:      toPubKey,
		Payload: payload,
	}
	data, _ := json.Marshal(msg)
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		return fmt.Errorf("send DM: %w", err)
	}

	fmt.Printf("DM sent to %s...\n  \"%s\"\n", toPubKey[:16], text)
	return nil
}

// ─── SMS commands ─────────────────────────────────────────────────────────────

// cmdSMSSend sends an outbound SMS via the COMMS server's Telnyx bridge.
func cmdSMSSend(server, profileName, to, text string) error {
	c, err := LoadProfile(profileName, server)
	if err != nil {
		return err
	}

	var result map[string]any
	resp, err := c.post("/api/v1/sms/send", map[string]string{
		"to":   to,
		"text": text,
	}, &result)
	if err != nil {
		return fmt.Errorf("send SMS: %w", err)
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("send failed (HTTP %d): %v", resp.StatusCode, result)
	}

	fmt.Println("SMS sent!")
	fmt.Printf("Message ID:        %v\n", result["message_id"])
	fmt.Printf("Telnyx Message ID: %v\n", result["telnyx_message_id"])
	fmt.Printf("From:              %v\n", result["from"])
	fmt.Printf("To:                %v\n", result["to"])
	return nil
}

// cmdSMSList prints the conversation history with a specific phone number.
func cmdSMSList(server, profileName, withNumber string) error {
	c, err := LoadProfile(profileName, server)
	if err != nil {
		return err
	}

	path := "/api/v1/sms/messages?with=" + url.QueryEscape(withNumber)
	var result map[string]any
	resp, err := c.get(path, &result)
	if err != nil {
		return fmt.Errorf("get messages: %w", err)
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("request failed (HTTP %d): %v", resp.StatusCode, result)
	}

	messages, _ := result["messages"].([]any)
	if len(messages) == 0 {
		fmt.Printf("No messages with %s.\n", withNumber)
		return nil
	}

	fmt.Printf("Messages with %s (%d total):\n\n", withNumber, len(messages))
	for _, m := range messages {
		msg, _ := m.(map[string]any)
		dir := "→ OUT"
		if msg["direction"] == "inbound" {
			dir = "← IN "
		}
		fmt.Printf("[%v] %s  %v\n", msg["created_at"], dir, msg["body"])
	}
	return nil
}

// cmdSMSConversations lists all unique SMS conversation partners with the most recent message.
func cmdSMSConversations(server, profileName string) error {
	c, err := LoadProfile(profileName, server)
	if err != nil {
		return err
	}

	var result map[string]any
	resp, err := c.get("/api/v1/sms/conversations", &result)
	if err != nil {
		return fmt.Errorf("get conversations: %w", err)
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("request failed (HTTP %d): %v", resp.StatusCode, result)
	}

	convs, _ := result["conversations"].([]any)
	if len(convs) == 0 {
		fmt.Println("No conversations.")
		return nil
	}

	fmt.Printf("%-16s  %-4s  %-32s  %s\n", "NUMBER", "DIR", "LAST MESSAGE", "TIME")
	fmt.Println(strings.Repeat("─", 72))
	for _, cv := range convs {
		m, _ := cv.(map[string]any)
		dir := "→"
		phone := fmt.Sprintf("%v", m["to_number"])
		if m["direction"] == "inbound" {
			dir = "←"
			phone = fmt.Sprintf("%v", m["from_number"])
		}
		body := fmt.Sprintf("%v", m["body"])
		if len(body) > 32 {
			body = body[:30] + "…"
		}
		createdAt := fmt.Sprintf("%v", m["created_at"])
		fmt.Printf("%-16s  %-4s  %-32s  %s\n", phone, dir, body, createdAt)
	}
	return nil
}

// ─── Legacy call commands (kept for backward compatibility) ───────────────────

// cmdCall initiates an outbound PSTN call via the server's SIP bridge (no audio).
// Use cmdDial for a full WebRTC call with audio.
func cmdCall(server, profileName, to string) error {
	c, err := LoadProfile(profileName, server)
	if err != nil {
		return err
	}

	var result map[string]any
	resp, err := c.post("/api/v1/calls/outbound", map[string]string{
		"to": to,
	}, &result)
	if err != nil {
		return fmt.Errorf("initiate call: %w", err)
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("call failed (HTTP %d): %v", resp.StatusCode, result)
	}

	callID := fmt.Sprintf("%v", result["call_id"])
	fmt.Println("Call initiated!")
	fmt.Printf("Call ID:    %s\n", callID)
	fmt.Printf("Bridge Key: %v\n", result["bridge_peer_key"])
	fmt.Printf("From DID:   %v\n", result["from_did"])
	fmt.Printf("\nTo hang up:  comms-cli hangup %s %s\n", profileName, callID)
	return nil
}

// cmdHangup hangs up an active call by ID.
func cmdHangup(server, profileName, callID string) error {
	c, err := LoadProfile(profileName, server)
	if err != nil {
		return err
	}

	var result map[string]any
	resp, err := c.post("/api/v1/calls/"+callID+"/hangup", nil, &result)
	if err != nil {
		return fmt.Errorf("hangup: %w", err)
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("hangup failed (HTTP %d): %v", resp.StatusCode, result)
	}

	fmt.Printf("Call %s ended.\n", callID)
	return nil
}

// ─── Full WebRTC call commands ────────────────────────────────────────────────

// cmdDial places an outbound PSTN call with full WebRTC audio.
func cmdDial(server, profileName, to string, audioCfg AudioConfig) error {
	c, err := LoadProfile(profileName, server)
	if err != nil {
		return err
	}

	// 1. Initiate call on server
	var result map[string]any
	resp, err := c.post("/api/v1/calls/outbound", map[string]string{"to": to}, &result)
	if err != nil {
		return fmt.Errorf("initiate call: %w", err)
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("call failed (HTTP %d): %v", resp.StatusCode, result)
	}

	callID := fmt.Sprintf("%v", result["call_id"])
	bridgeKey := fmt.Sprintf("%v", result["bridge_peer_key"])
	fromDID := fmt.Sprintf("%v", result["from_did"])
	fmt.Printf("Calling %s from %s (call_id=%s)\n", to, fromDID, callID)
	fmt.Println("Use Ctrl+C to hang up.")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	return runWebRTCCall(ctx, c, bridgeKey, callID, audioCfg)
}

// cmdAnswer waits for an inbound call and answers it with full WebRTC audio.
func cmdAnswer(server, profileName string, audioCfg AudioConfig) error {
	c, err := LoadProfile(profileName, server)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// Connect to signaling WS
	fmt.Printf("Connecting to %s ...\n", c.wsURL())
	conn, err := c.Connect(ctx)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	router := newSignalingRouter()
	startWSReader(ctx, conn, router)

	fmt.Printf("Ready  ✓  profile=%s  pubkey=%.16s...\n", profileName, c.identity.PublicKeyHex())
	fmt.Println("Waiting for incoming call... (Ctrl+C to cancel)")
	fmt.Println(strings.Repeat("─", 60))

	// Wait for incoming_call signal
	var incomingPayload incomingCallPayload
	select {
	case <-ctx.Done():
		fmt.Println("\nCancelled.")
		return nil
	case raw := <-router.incomingCallCh:
		if err := json.Unmarshal(raw, &incomingPayload); err != nil {
			return fmt.Errorf("parse incoming_call payload: %w", err)
		}
	}

	fmt.Printf("\nIncoming call from %s → DID %s\n",
		incomingPayload.CallerNumber, incomingPayload.CalledDID)
	fmt.Printf("Call ID: %s\n", incomingPayload.CallID)
	fmt.Println("Answering...")

	ws := &wsWriter{conn: conn}

	// Create pion peer connection
	cp, err := newCLIPeerConnection()
	if err != nil {
		return fmt.Errorf("create peer connection: %w", err)
	}
	defer cp.pc.Close()

	// Prepare audio source
	samples, err := prepareAudioSource(audioCfg)
	if err != nil {
		return err
	}

	// Perform offer/answer exchange with bridge
	if err := doWebRTCOfferAnswer(ctx, cp, ws, c.identity.PublicKeyHex(), incomingPayload.BridgeKey, router); err != nil {
		return fmt.Errorf("WebRTC negotiation: %w", err)
	}

	return runCallSession(ctx, conn, cp, ws, incomingPayload.BridgeKey, incomingPayload.CallID, c, samples, audioCfg, router)
}

// cmdTranscribe transcribes a WAV file using the whisper CLI.
func cmdTranscribe(_ string, filePath string) error {
	if _, err := os.Stat(filePath); err != nil {
		return fmt.Errorf("file not found: %s", filePath)
	}
	return runSTT(filePath)
}

// ─── Internal helpers ─────────────────────────────────────────────────────────

// prepareAudioSource loads or generates the audio samples to send.
// Returns nil samples if no audio source is configured (sends silence).
func prepareAudioSource(audioCfg AudioConfig) ([]int16, error) {
	if audioCfg.TTSText != "" {
		fmt.Printf("Generating TTS: %q\n", audioCfg.TTSText)
		wavPath, err := generateTTS(audioCfg.TTSText)
		if err != nil {
			return nil, fmt.Errorf("TTS generation: %w", err)
		}
		samples, err := readWAV(wavPath)
		if err != nil {
			return nil, fmt.Errorf("read TTS audio: %w", err)
		}
		durationSec := float64(len(samples)) / 8000.0
		fmt.Printf("TTS audio: %.1fs\n", durationSec)
		return samples, nil
	}

	if audioCfg.AudioFile != "" {
		samples, err := readWAV(audioCfg.AudioFile)
		if err != nil {
			return nil, fmt.Errorf("read audio file: %w", err)
		}
		durationSec := float64(len(samples)) / 8000.0
		fmt.Printf("Audio file: %s (%.1fs)\n", audioCfg.AudioFile, durationSec)
		return samples, nil
	}

	return nil, nil // no audio source → silence
}

// runWebRTCCall handles the WebRTC call session for cmdDial.
// Connects WS, negotiates WebRTC, runs audio pipeline.
func runWebRTCCall(ctx context.Context, c *CommsClient, bridgeKey, callID string, audioCfg AudioConfig) error {
	// Connect to signaling WS
	conn, err := c.Connect(ctx)
	if err != nil {
		return fmt.Errorf("connect WebSocket: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	router := newSignalingRouter()
	startWSReader(ctx, conn, router)

	ws := &wsWriter{conn: conn}

	// Create pion peer connection
	cp, err := newCLIPeerConnection()
	if err != nil {
		return fmt.Errorf("create peer connection: %w", err)
	}
	defer cp.pc.Close()

	// Prepare audio source
	samples, err := prepareAudioSource(audioCfg)
	if err != nil {
		return err
	}

	// Perform offer/answer exchange
	if err := doWebRTCOfferAnswer(ctx, cp, ws, c.identity.PublicKeyHex(), bridgeKey, router); err != nil {
		return fmt.Errorf("WebRTC negotiation: %w", err)
	}

	return runCallSession(ctx, conn, cp, ws, bridgeKey, callID, c, samples, audioCfg, router)
}

// runCallSession runs the audio pipeline after WebRTC negotiation is complete.
// Blocks until the call ends.
func runCallSession(
	ctx context.Context,
	conn *websocket.Conn,
	cp *cliPeerConnection,
	ws *wsWriter,
	bridgeKey, callID string,
	c *CommsClient,
	samples []int16,
	audioCfg AudioConfig,
	router *signalingRouter,
) error {
	// Wait for WebRTC connection
	fmt.Println("Waiting for WebRTC connection...")
	connTimeout := time.After(30 * time.Second)
	select {
	case <-ctx.Done():
		return nil
	case <-connTimeout:
		return fmt.Errorf("WebRTC connection timeout")
	case <-cp.connected:
		fmt.Println("WebRTC connected!")
	}

	// Determine recording path
	recordPath := audioCfg.RecordTo
	if audioCfg.AutoSTT && recordPath == "" {
		recordPath = filepath.Join(os.TempDir(), "comms_recv.wav")
	}

	// Wait for remote audio track
	var rec *recorder
	trackTimeout := time.After(10 * time.Second)
	select {
	case <-ctx.Done():
		return nil
	case <-trackTimeout:
		fmt.Println("Warning: no remote audio track received, call may be one-way")
	case remoteTrack := <-cp.remoteTrack:
		if recordPath != "" {
			fmt.Printf("Recording received audio to %s\n", recordPath)
			rec = startRecording(ctx, remoteTrack)
		} else {
			// Still need to drain RTP packets to avoid blocking the connection
			go func() {
				for {
					_, _, err := remoteTrack.ReadRTP()
					if err != nil {
						return
					}
				}
			}()
		}
	}

	// Start audio sender
	audioCtx, audioCancel := context.WithCancel(ctx)
	audioEnded := make(chan struct{})
	go func() {
		defer close(audioEnded)
		if samples != nil {
			fmt.Println("Sending audio...")
			sendAudio(audioCtx, cp.audioTrack, samples) //nolint:errcheck
		} else {
			fmt.Println("Connected (sending silence, Ctrl+C to hang up)")
			sendSilence(audioCtx, cp.audioTrack)
		}
	}()

	// Block until call ends
	var reason string
	select {
	case <-ctx.Done():
		reason = "user cancelled"
	case <-cp.ended:
		reason = "WebRTC ended"
	case <-router.hangupCh:
		reason = "remote hangup"
	case <-audioEnded:
		reason = "audio source exhausted"
	}

	audioCancel()
	fmt.Printf("\nCall ended: %s\n", reason)

	// Hang up on server
	if callID != "" {
		c.post("/api/v1/calls/"+callID+"/hangup", nil, nil) //nolint:errcheck
	}
	// Send hangup signaling
	ws.send(ctx, signaling.Message{ //nolint:errcheck
		Type: signaling.MsgTypeHangup,
		From: c.identity.PublicKeyHex(),
		To:   bridgeKey,
	})

	// Write recording
	if rec != nil && recordPath != "" {
		if err := rec.writeWAVFile(recordPath); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: write recording: %v\n", err)
		} else if audioCfg.AutoSTT {
			runSTT(recordPath) //nolint:errcheck
		}
	}

	return nil
}
