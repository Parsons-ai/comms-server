package dashboard

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Parsons-ai/comms-server/internal/identity"
	"github.com/Parsons-ai/comms-server/internal/store"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func setupDashboard(t *testing.T) (*httptest.Server, *store.DB) {
	t.Helper()
	db, err := store.Open(":memory:", testLogger())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	id, _ := identity.Generate()
	dash := New(db, id, "0.1.0-test", "8080", testLogger())
	ts := httptest.NewServer(dash.Handler())
	t.Cleanup(func() {
		ts.Close()
		db.Close()
	})
	return ts, db
}

func TestDashboardHTML(t *testing.T) {
	ts, _ := setupDashboard(t)

	resp, err := ts.Client().Get(ts.URL + "/dashboard")
	if err != nil {
		t.Fatalf("GET /dashboard: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("content-type: got %s, want text/html", ct)
	}
}

func TestDashboardAPIStatus(t *testing.T) {
	ts, db := setupDashboard(t)

	// Add some test data
	db.Exec("INSERT INTO users (public_key, display_name) VALUES ('key1', 'User 1')")
	db.Exec("INSERT INTO users (public_key, display_name) VALUES ('key2', 'User 2')")

	resp, err := ts.Client().Get(ts.URL + "/dashboard/api/status")
	if err != nil {
		t.Fatalf("GET /dashboard/api/status: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}

	var result statusData
	json.NewDecoder(resp.Body).Decode(&result)

	if result.Version != "0.1.0-test" {
		t.Errorf("version: got %s, want 0.1.0-test", result.Version)
	}
	if result.UserCount != 2 {
		t.Errorf("user_count: got %d, want 2", result.UserCount)
	}
	if len(result.PublicKey) != 64 {
		t.Errorf("public_key length: got %d, want 64", len(result.PublicKey))
	}
}

func TestDashboardMount(t *testing.T) {
	db, err := store.Open(":memory:", testLogger())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	id, _ := identity.Generate()
	dash := New(db, id, "0.1.0", "8080", testLogger())

	mux := http.NewServeMux()
	dash.Mount(mux)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, _ := ts.Client().Get(ts.URL + "/dashboard")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("mounted dashboard status: got %d, want 200", resp.StatusCode)
	}
}
