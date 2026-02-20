package triage

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Parsons-ai/comms-server/internal/store"
)

// Decision represents the AI's decision about how to handle an incoming call.
type Decision struct {
	Action     string `json:"action"`      // ring, voicemail, reject, transfer
	Reason     string `json:"reason"`      // human-readable reason for the decision
	Priority   int    `json:"priority"`    // 1=urgent, 2=important, 3=normal, 4=low
	Confidence float64 `json:"confidence"` // 0.0-1.0 confidence in the decision
}

// CallerInfo is the context about an incoming call provided to the triage engine.
type CallerInfo struct {
	CallerKey   string `json:"caller_key"`   // COMMS public key (if known)
	CallerPhone string `json:"caller_phone"` // phone number (if from PSTN)
	CallerName  string `json:"caller_name"`  // display name (if in contacts)
	IsContact   bool   `json:"is_contact"`   // whether caller is in user's contacts
	IsBlocked   bool   `json:"is_blocked"`   // whether caller is blocked
	TimeOfDay   string `json:"time_of_day"`  // morning, afternoon, evening, night
	DayOfWeek   string `json:"day_of_week"`  // Monday, Tuesday, etc.
}

// UserConfig is the user's triage preferences.
type UserConfig struct {
	Greeting       string   `json:"greeting"`
	Personality    string   `json:"personality"`
	EnabledTools   []string `json:"enabled_tools"`
	MaxDurationSec int      `json:"max_duration_sec"`
}

// Engine is the AI triage engine that decides how to handle incoming calls.
// In v1, it uses rule-based logic with routing_rules table lookups.
// Future versions will integrate with an LLM for more sophisticated screening.
type Engine struct {
	db     *store.DB
	logger *slog.Logger
}

// New creates a new triage engine.
func New(db *store.DB, logger *slog.Logger) *Engine {
	return &Engine{
		db:     db,
		logger: logger.With("service", "triage"),
	}
}

// Evaluate decides how to handle an incoming call based on routing rules
// and caller context.
func (e *Engine) Evaluate(ctx context.Context, userID int64, caller CallerInfo) (*Decision, error) {
	// Step 1: Blocked callers are always rejected
	if caller.IsBlocked {
		return &Decision{
			Action:     "reject",
			Reason:     "caller is blocked",
			Priority:   4,
			Confidence: 1.0,
		}, nil
	}

	// Step 2: Check routing rules (ordered by priority)
	rules, err := e.loadRules(userID)
	if err != nil {
		return nil, fmt.Errorf("load routing rules: %w", err)
	}

	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		if e.matchesConditions(caller, rule.Conditions) {
			return &Decision{
				Action:     rule.Action,
				Reason:     fmt.Sprintf("matched rule: %s", rule.Name),
				Priority:   e.inferPriority(caller, rule),
				Confidence: 0.9,
			}, nil
		}
	}

	// Step 3: Default behavior
	if caller.IsContact {
		return &Decision{
			Action:     "ring",
			Reason:     "known contact",
			Priority:   2,
			Confidence: 0.8,
		}, nil
	}

	// Unknown caller — default to AI screening if configured, otherwise ring
	config, err := e.loadConfig(userID)
	if err != nil || config == nil {
		return &Decision{
			Action:     "ring",
			Reason:     "no triage config, default ring",
			Priority:   3,
			Confidence: 0.5,
		}, nil
	}

	return &Decision{
		Action:     "ai_screen",
		Reason:     "unknown caller, AI screening enabled",
		Priority:   3,
		Confidence: 0.7,
	}, nil
}

// LogConversation saves an AI triage conversation to the database.
func (e *Engine) LogConversation(userID, callID int64, model, transcript, outcome string) (int64, error) {
	result, err := e.db.Exec(
		`INSERT INTO ai_conversations (user_id, call_id, model, transcript, outcome, ended_at)
		 VALUES (?, ?, ?, ?, ?, datetime('now'))`,
		userID, callID, model, transcript, outcome,
	)
	if err != nil {
		return 0, fmt.Errorf("log conversation: %w", err)
	}
	id, _ := result.LastInsertId()
	return id, nil
}

// GetConfig returns the user's AI triage configuration.
func (e *Engine) GetConfig(userID int64) (*UserConfig, error) {
	return e.loadConfig(userID)
}

// SetConfig creates or updates the user's AI triage configuration.
func (e *Engine) SetConfig(userID int64, config UserConfig) error {
	tools, _ := json.Marshal(config.EnabledTools)
	_, err := e.db.Exec(
		`INSERT INTO ai_config (user_id, greeting, personality, tools, max_duration_sec)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET
		   greeting = excluded.greeting,
		   personality = excluded.personality,
		   tools = excluded.tools,
		   max_duration_sec = excluded.max_duration_sec,
		   updated_at = datetime('now')`,
		userID, config.Greeting, config.Personality, string(tools), config.MaxDurationSec,
	)
	return err
}

// --- Internal helpers ---

type routingRule struct {
	Priority   int
	Name       string
	Conditions map[string]any
	Action     string
	Enabled    bool
}

func (e *Engine) loadRules(userID int64) ([]routingRule, error) {
	rows, err := e.db.Query(
		`SELECT priority, name, conditions, action, enabled
		 FROM routing_rules WHERE user_id = ? ORDER BY priority`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rules []routingRule
	for rows.Next() {
		var r routingRule
		var condJSON string
		var enabled int
		if err := rows.Scan(&r.Priority, &r.Name, &condJSON, &r.Action, &enabled); err != nil {
			return nil, err
		}
		r.Enabled = enabled == 1
		json.Unmarshal([]byte(condJSON), &r.Conditions)
		rules = append(rules, r)
	}
	return rules, nil
}

func (e *Engine) matchesConditions(caller CallerInfo, conditions map[string]any) bool {
	if conditions == nil || len(conditions) == 0 {
		return true // empty conditions match everything
	}

	for key, val := range conditions {
		switch key {
		case "is_contact":
			if v, ok := val.(bool); ok && v != caller.IsContact {
				return false
			}
		case "caller_group":
			// Future: check if caller belongs to a named group
			// For now, treat "family" etc. as matching contacts
			if val == "family" && !caller.IsContact {
				return false
			}
		case "time_of_day":
			if v, ok := val.(string); ok && !strings.EqualFold(v, caller.TimeOfDay) {
				return false
			}
		case "day_of_week":
			if v, ok := val.(string); ok && !strings.EqualFold(v, caller.DayOfWeek) {
				return false
			}
		case "has_phone":
			if v, ok := val.(bool); ok {
				hasPhone := caller.CallerPhone != ""
				if v != hasPhone {
					return false
				}
			}
		}
	}
	return true
}

func (e *Engine) inferPriority(caller CallerInfo, rule routingRule) int {
	if caller.IsContact {
		return 2
	}
	if rule.Action == "reject" {
		return 4
	}
	return 3
}

func (e *Engine) loadConfig(userID int64) (*UserConfig, error) {
	var config UserConfig
	var toolsJSON string
	err := e.db.QueryRow(
		`SELECT greeting, personality, tools, max_duration_sec
		 FROM ai_config WHERE user_id = ?`, userID,
	).Scan(&config.Greeting, &config.Personality, &toolsJSON, &config.MaxDurationSec)
	if err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(toolsJSON), &config.EnabledTools)
	return &config, nil
}

// TimeOfDay returns a string describing the current time of day.
func TimeOfDay(t time.Time) string {
	h := t.Hour()
	switch {
	case h < 6:
		return "night"
	case h < 12:
		return "morning"
	case h < 18:
		return "afternoon"
	default:
		return "evening"
	}
}
