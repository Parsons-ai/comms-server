package dashboard

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"runtime"
	"time"

	"github.com/Parsons-ai/comms-server/internal/identity"
	"github.com/Parsons-ai/comms-server/internal/store"
)

// Server is the web dashboard server. It embeds into the API server's port.
type Server struct {
	logger   *slog.Logger
	db       *store.DB
	identity *identity.Identity
	mux      *http.ServeMux
	started  time.Time
	version  string
}

// New creates a new dashboard.
func New(db *store.DB, id *identity.Identity, version string, logger *slog.Logger) *Server {
	s := &Server{
		logger:   logger.With("service", "dashboard"),
		db:       db,
		identity: id,
		started:  time.Now(),
		version:  version,
	}

	s.mux = http.NewServeMux()
	s.mux.HandleFunc("GET /dashboard", s.handleDashboard)
	s.mux.HandleFunc("GET /dashboard/api/status", s.handleAPIStatus)

	return s
}

// Mount registers dashboard routes onto an existing mux.
func (s *Server) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /dashboard", s.handleDashboard)
	mux.HandleFunc("GET /dashboard/api/status", s.handleAPIStatus)
}

// Handler returns the HTTP handler for testing.
func (s *Server) Handler() http.Handler {
	return s.mux
}

// statusData holds data for the dashboard template and API.
type statusData struct {
	Version   string `json:"version"`
	PublicKey string `json:"public_key"`
	ShortID   string `json:"short_id"`
	Uptime    string `json:"uptime"`
	GoVersion string `json:"go_version"`
	Platform  string `json:"platform"`

	UserCount    int `json:"user_count"`
	DeviceCount  int `json:"device_count"`
	MessageCount int `json:"pending_messages"`
	CallCount    int `json:"total_calls"`
	ContactCount int `json:"contact_count"`
	RuleCount    int `json:"routing_rules"`
}

func (s *Server) getStatus() statusData {
	data := statusData{
		Version:   s.version,
		PublicKey: s.identity.PublicKeyHex(),
		ShortID:   s.identity.ShortID(),
		Uptime:    time.Since(s.started).Round(time.Second).String(),
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}

	s.db.QueryRow("SELECT count(*) FROM users").Scan(&data.UserCount)
	s.db.QueryRow("SELECT count(*) FROM devices").Scan(&data.DeviceCount)
	s.db.QueryRow("SELECT count(*) FROM messages WHERE delivered = 0").Scan(&data.MessageCount)
	s.db.QueryRow("SELECT count(*) FROM call_log").Scan(&data.CallCount)
	s.db.QueryRow("SELECT count(*) FROM contacts").Scan(&data.ContactCount)
	s.db.QueryRow("SELECT count(*) FROM routing_rules").Scan(&data.RuleCount)

	return data
}

func (s *Server) handleAPIStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.getStatus())
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	data := s.getStatus()

	tmpl, err := template.New("dashboard").Parse(dashboardHTML)
	if err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		s.logger.Error("dashboard template", "error", err)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, data); err != nil {
		s.logger.Error("render dashboard", "error", err)
	}
}

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>COMMS Server Dashboard</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', system-ui, sans-serif;
            background: #0a0a0a;
            color: #e5e5e5;
            min-height: 100vh;
        }
        .header {
            background: #111;
            border-bottom: 1px solid #222;
            padding: 1.5rem 2rem;
            display: flex;
            align-items: center;
            gap: 1rem;
        }
        .header h1 {
            font-size: 1.5rem;
            font-weight: 600;
            color: #fff;
        }
        .header .version {
            background: #1a3a2a;
            color: #4ade80;
            padding: 0.25rem 0.75rem;
            border-radius: 9999px;
            font-size: 0.75rem;
            font-weight: 500;
        }
        .container {
            max-width: 960px;
            margin: 2rem auto;
            padding: 0 1.5rem;
        }
        .card {
            background: #111;
            border: 1px solid #222;
            border-radius: 0.75rem;
            padding: 1.5rem;
            margin-bottom: 1.5rem;
        }
        .card h2 {
            font-size: 0.875rem;
            font-weight: 500;
            color: #888;
            text-transform: uppercase;
            letter-spacing: 0.05em;
            margin-bottom: 1rem;
        }
        .identity-grid {
            display: grid;
            grid-template-columns: auto 1fr;
            gap: 0.5rem 1.5rem;
            font-size: 0.875rem;
        }
        .identity-grid dt { color: #888; }
        .identity-grid dd {
            font-family: 'SF Mono', 'Fira Code', monospace;
            color: #fff;
            word-break: break-all;
        }
        .stats-grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
            gap: 1rem;
        }
        .stat {
            background: #0a0a0a;
            border: 1px solid #1a1a1a;
            border-radius: 0.5rem;
            padding: 1rem;
            text-align: center;
        }
        .stat-value {
            font-size: 2rem;
            font-weight: 700;
            color: #fff;
        }
        .stat-label {
            font-size: 0.75rem;
            color: #666;
            margin-top: 0.25rem;
        }
        .footer {
            text-align: center;
            padding: 2rem;
            color: #444;
            font-size: 0.75rem;
        }
        .footer a { color: #4ade80; text-decoration: none; }
        .footer a:hover { text-decoration: underline; }
        @media (max-width: 640px) {
            .stats-grid { grid-template-columns: repeat(2, 1fr); }
        }
    </style>
</head>
<body>
    <div class="header">
        <h1>COMMS Server</h1>
        <span class="version">v{{.Version}}</span>
    </div>
    <div class="container">
        <div class="card">
            <h2>Server Identity</h2>
            <dl class="identity-grid">
                <dt>Public Key</dt>
                <dd>{{.PublicKey}}</dd>
                <dt>Short ID</dt>
                <dd>{{.ShortID}}</dd>
                <dt>Uptime</dt>
                <dd id="uptime">{{.Uptime}}</dd>
                <dt>Platform</dt>
                <dd>{{.Platform}} ({{.GoVersion}})</dd>
            </dl>
        </div>
        <div class="card">
            <h2>Statistics</h2>
            <div class="stats-grid">
                <div class="stat">
                    <div class="stat-value">{{.UserCount}}</div>
                    <div class="stat-label">Users</div>
                </div>
                <div class="stat">
                    <div class="stat-value">{{.DeviceCount}}</div>
                    <div class="stat-label">Devices</div>
                </div>
                <div class="stat">
                    <div class="stat-value">{{.MessageCount}}</div>
                    <div class="stat-label">Pending Messages</div>
                </div>
                <div class="stat">
                    <div class="stat-value">{{.CallCount}}</div>
                    <div class="stat-label">Total Calls</div>
                </div>
                <div class="stat">
                    <div class="stat-value">{{.ContactCount}}</div>
                    <div class="stat-label">Contacts</div>
                </div>
                <div class="stat">
                    <div class="stat-value">{{.RuleCount}}</div>
                    <div class="stat-label">Routing Rules</div>
                </div>
            </div>
        </div>
    </div>
    <div class="footer">
        COMMS &mdash; Open Source Secure Communications<br>
        <a href="https://github.com/Parsons-ai/comms" target="_blank">github.com/Parsons-ai/comms</a>
    </div>
    <script>
        // Auto-refresh stats every 10s
        setInterval(async () => {
            try {
                const resp = await fetch('/dashboard/api/status');
                const data = await resp.json();
                document.getElementById('uptime').textContent = data.uptime;
                const values = document.querySelectorAll('.stat-value');
                const keys = ['user_count','device_count','pending_messages','total_calls','contact_count','routing_rules'];
                keys.forEach((k, i) => { if (values[i]) values[i].textContent = data[k]; });
            } catch(e) {}
        }, 10000);
    </script>
</body>
</html>` + "\n"

// Addr returns the dashboard URL.
func (s *Server) Addr() string {
	return fmt.Sprintf("Dashboard at /dashboard")
}
