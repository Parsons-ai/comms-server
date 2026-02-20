package api

import (
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/Parsons-ai/comms-server/internal/identity"
)

// signRequest adds auth headers to an HTTP request using the given identity.
func signRequest(r *http.Request, id *identity.Identity) {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	payload := fmt.Sprintf("%s:%s:%s", ts, r.Method, r.URL.Path)
	sig := id.Sign([]byte(payload))

	r.Header.Set(HeaderPublicKey, id.PublicKeyHex())
	r.Header.Set(HeaderSignature, hex.EncodeToString(sig))
	r.Header.Set(HeaderTimestamp, ts)
}

func TestPublicEndpointsNoAuth(t *testing.T) {
	ts, _, _ := setupAPI(t)

	// These should work without auth headers
	endpoints := []string{
		"/api/v1/health",
		"/api/v1/identity",
		"/api/v1/ice-servers",
	}

	for _, ep := range endpoints {
		resp, err := ts.Client().Get(ts.URL + ep)
		if err != nil {
			t.Fatalf("GET %s: %v", ep, err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("GET %s: got %d, want 200", ep, resp.StatusCode)
		}
	}
}

func TestProtectedEndpointRequiresAuth(t *testing.T) {
	ts, _, _ := setupAPI(t)

	// GET /api/v1/users should require auth
	resp, err := ts.Client().Get(ts.URL + "/api/v1/users")
	if err != nil {
		t.Fatalf("GET /users: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("GET /users without auth: got %d, want 401", resp.StatusCode)
	}
}

func TestAuthWithValidSignature(t *testing.T) {
	ts, _, _ := setupAPI(t)

	// Register a user first (public endpoint, no auth needed)
	clientID, _ := identity.Generate()
	createTestUser(t, ts, clientID.PublicKeyHex(), "AuthUser")

	// Now make an authenticated request
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/users", nil)
	signRequest(req, clientID)

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /users with auth: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("GET /users with auth: got %d, want 200", resp.StatusCode)
	}
}

func TestAuthWithInvalidSignature(t *testing.T) {
	ts, _, _ := setupAPI(t)

	clientID, _ := identity.Generate()
	createTestUser(t, ts, clientID.PublicKeyHex(), "AuthUser2")

	// Sign with a different identity
	otherID, _ := identity.Generate()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/users", nil)
	// Use client's public key but sign with other's private key
	tsVal := strconv.FormatInt(time.Now().Unix(), 10)
	payload := fmt.Sprintf("%s:%s:%s", tsVal, req.Method, "/api/v1/users")
	sig := otherID.Sign([]byte(payload))

	req.Header.Set(HeaderPublicKey, clientID.PublicKeyHex())
	req.Header.Set(HeaderSignature, hex.EncodeToString(sig))
	req.Header.Set(HeaderTimestamp, tsVal)

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /users with bad sig: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("GET /users with bad sig: got %d, want 401", resp.StatusCode)
	}
}

func TestAuthWithExpiredTimestamp(t *testing.T) {
	ts, _, _ := setupAPI(t)

	clientID, _ := identity.Generate()
	createTestUser(t, ts, clientID.PublicKeyHex(), "AuthUser3")

	// Use a timestamp 120 seconds in the past
	oldTS := strconv.FormatInt(time.Now().Unix()-120, 10)
	payload := fmt.Sprintf("%s:%s:%s", oldTS, "GET", "/api/v1/users")
	sig := clientID.Sign([]byte(payload))

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/users", nil)
	req.Header.Set(HeaderPublicKey, clientID.PublicKeyHex())
	req.Header.Set(HeaderSignature, hex.EncodeToString(sig))
	req.Header.Set(HeaderTimestamp, oldTS)

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /users with old timestamp: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("GET /users with old timestamp: got %d, want 401", resp.StatusCode)
	}
}

func TestAuthWithUnknownKey(t *testing.T) {
	ts, _, _ := setupAPI(t)

	// Don't register this identity
	unknownID, _ := identity.Generate()

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/users", nil)
	signRequest(req, unknownID)

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /users with unknown key: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("GET /users with unknown key: got %d, want 401", resp.StatusCode)
	}
}

func TestRegistrationIsPublic(t *testing.T) {
	ts, _, _ := setupAPI(t)

	// POST /api/v1/users should work without auth
	newID, _ := identity.Generate()
	id := createTestUser(t, ts, newID.PublicKeyHex(), "NewUser")
	if id == 0 {
		t.Error("expected non-zero user id from unauthenticated registration")
	}
}
