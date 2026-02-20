package api

import (
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/Parsons-ai/comms-server/internal/identity"
)

const (
	// HeaderPublicKey is the hex-encoded Ed25519 public key of the client.
	HeaderPublicKey = "X-COMMS-Key"
	// HeaderSignature is the hex-encoded signature of the request payload.
	HeaderSignature = "X-COMMS-Sig"
	// HeaderTimestamp is the Unix timestamp (seconds) when the request was signed.
	HeaderTimestamp = "X-COMMS-Time"

	// maxClockSkew is the maximum allowed difference between the request
	// timestamp and the server's current time.
	maxClockSkew = 60 // seconds
)

// publicEndpoints lists routes that do NOT require authentication.
var publicEndpoints = map[string]bool{
	"GET /api/v1/health":     true,
	"GET /api/v1/identity":   true,
	"GET /api/v1/ice-servers": true,
	"POST /api/v1/users":     true, // registration
}

// authMiddleware wraps the API mux and enforces Ed25519 signature auth
// on all endpoints except those in publicEndpoints.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check if this is a public endpoint
		routeKey := r.Method + " " + r.URL.Path
		if publicEndpoints[routeKey] {
			next.ServeHTTP(w, r)
			return
		}

		// Also allow dashboard and signaling endpoints through
		if len(r.URL.Path) >= 10 && r.URL.Path[:10] == "/dashboard" {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/ws" {
			next.ServeHTTP(w, r)
			return
		}

		// Extract auth headers
		pubKeyHex := r.Header.Get(HeaderPublicKey)
		sigHex := r.Header.Get(HeaderSignature)
		tsStr := r.Header.Get(HeaderTimestamp)

		if pubKeyHex == "" || sigHex == "" || tsStr == "" {
			s.json(w, http.StatusUnauthorized, map[string]string{
				"error": "missing auth headers: X-COMMS-Key, X-COMMS-Sig, X-COMMS-Time required",
			})
			return
		}

		// Parse timestamp and check clock skew
		ts, err := strconv.ParseInt(tsStr, 10, 64)
		if err != nil {
			s.json(w, http.StatusUnauthorized, map[string]string{
				"error": "invalid timestamp",
			})
			return
		}
		diff := time.Now().Unix() - ts
		if math.Abs(float64(diff)) > maxClockSkew {
			s.json(w, http.StatusUnauthorized, map[string]string{
				"error": "timestamp too far from server time",
			})
			return
		}

		// Parse public key
		pubKey, err := identity.PublicKeyFromHex(pubKeyHex)
		if err != nil {
			s.json(w, http.StatusUnauthorized, map[string]string{
				"error": "invalid public key: " + err.Error(),
			})
			return
		}

		// Verify the user exists
		var userID int64
		if err := s.db.QueryRow("SELECT id FROM users WHERE public_key = ?", pubKeyHex).Scan(&userID); err != nil {
			s.json(w, http.StatusUnauthorized, map[string]string{
				"error": "unknown public key",
			})
			return
		}

		// Build the signed payload: timestamp + method + path
		payload := fmt.Sprintf("%s:%s:%s", tsStr, r.Method, r.URL.Path)

		// Decode and verify signature
		sig, err := hex.DecodeString(sigHex)
		if err != nil {
			s.json(w, http.StatusUnauthorized, map[string]string{
				"error": "invalid signature encoding",
			})
			return
		}

		if !identity.Verify(pubKey, []byte(payload), sig) {
			s.json(w, http.StatusUnauthorized, map[string]string{
				"error": "signature verification failed",
			})
			return
		}

		// Update last_seen
		s.db.Exec("UPDATE users SET last_seen = datetime('now') WHERE id = ?", userID)

		next.ServeHTTP(w, r)
	})
}
