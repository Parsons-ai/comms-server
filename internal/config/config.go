package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config holds all server configuration.
// Precedence: CLI flags > env vars > YAML file > defaults.
type Config struct {
	// Server identity
	DataDir  string `yaml:"data_dir"`
	NodeName string `yaml:"node_name"`

	// Network
	ListenAddr    string `yaml:"listen_addr"`
	WSPort        int    `yaml:"ws_port"`
	APIPort       int    `yaml:"api_port"`
	DashboardPort int    `yaml:"dashboard_port"`

	// DHT
	DHTPort       int      `yaml:"dht_port"`
	DHTBootstrap  []string `yaml:"dht_bootstrap"`

	// SIP / VoIP (optional)
	SIPEnabled  bool   `yaml:"sip_enabled"`
	SIPPort     int    `yaml:"sip_port"`
	SIPProvider string `yaml:"sip_provider"` // telnyx, voipms, signalwire, generic
	SIPUsername string `yaml:"sip_username"`
	SIPPassword string `yaml:"sip_password"`
	SIPDomain   string `yaml:"sip_domain"`

	// AI Triage (optional)
	TriageEnabled bool   `yaml:"triage_enabled"`
	TriageModel   string `yaml:"triage_model"` // anthropic:claude-sonnet, local:llama, etc.

	// Relay contribution
	RelayEnabled    bool `yaml:"relay_enabled"`
	RelayMaxBwMbps  int  `yaml:"relay_max_bw_mbps"`
	MailboxMaxMB    int  `yaml:"mailbox_max_mb"`
	MailboxMaxUsers int  `yaml:"mailbox_max_users"`

	// Logging
	LogLevel  string `yaml:"log_level"` // debug, info, warn, error
	LogFormat string `yaml:"log_format"` // text, json
}

// Default returns a Config with sensible defaults for a Raspberry Pi.
func Default() *Config {
	homeDir, _ := os.UserHomeDir()
	return &Config{
		DataDir:  filepath.Join(homeDir, ".comms"),
		NodeName: "",

		ListenAddr:    "0.0.0.0",
		WSPort:        8443,
		APIPort:       8080,
		DashboardPort: 8080, // same as API, served under /dashboard

		DHTPort:      4001,
		DHTBootstrap: []string{},

		SIPEnabled:  false,
		SIPPort:     5060,
		SIPProvider: "generic",

		TriageEnabled: false,
		TriageModel:   "",

		RelayEnabled:    true,
		RelayMaxBwMbps:  10,
		MailboxMaxMB:    512,
		MailboxMaxUsers: 50,

		LogLevel:  "info",
		LogFormat: "text",
	}
}

// LoadFromFile reads a YAML config file and merges with defaults.
func LoadFromFile(path string) (*Config, error) {
	cfg := Default()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	return cfg, nil
}

// ApplyEnv overrides config values with environment variables.
// Format: COMMS_<FIELD_NAME> (e.g., COMMS_WS_PORT=9443)
func (c *Config) ApplyEnv() {
	if v := os.Getenv("COMMS_DATA_DIR"); v != "" {
		c.DataDir = v
	}
	if v := os.Getenv("COMMS_NODE_NAME"); v != "" {
		c.NodeName = v
	}
	if v := os.Getenv("COMMS_LISTEN_ADDR"); v != "" {
		c.ListenAddr = v
	}
	if v := os.Getenv("COMMS_WS_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			c.WSPort = p
		}
	}
	if v := os.Getenv("COMMS_API_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			c.APIPort = p
		}
	}
	if v := os.Getenv("COMMS_DHT_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			c.DHTPort = p
		}
	}
	if v := os.Getenv("COMMS_DHT_BOOTSTRAP"); v != "" {
		c.DHTBootstrap = strings.Split(v, ",")
	}
	if v := os.Getenv("COMMS_SIP_ENABLED"); v != "" {
		c.SIPEnabled = v == "true" || v == "1"
	}
	if v := os.Getenv("COMMS_SIP_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			c.SIPPort = p
		}
	}
	if v := os.Getenv("COMMS_SIP_PROVIDER"); v != "" {
		c.SIPProvider = v
	}
	if v := os.Getenv("COMMS_SIP_USERNAME"); v != "" {
		c.SIPUsername = v
	}
	if v := os.Getenv("COMMS_SIP_PASSWORD"); v != "" {
		c.SIPPassword = v
	}
	if v := os.Getenv("COMMS_SIP_DOMAIN"); v != "" {
		c.SIPDomain = v
	}
	if v := os.Getenv("COMMS_TRIAGE_ENABLED"); v != "" {
		c.TriageEnabled = v == "true" || v == "1"
	}
	if v := os.Getenv("COMMS_TRIAGE_MODEL"); v != "" {
		c.TriageModel = v
	}
	if v := os.Getenv("COMMS_LOG_LEVEL"); v != "" {
		c.LogLevel = v
	}
	if v := os.Getenv("COMMS_LOG_FORMAT"); v != "" {
		c.LogFormat = v
	}
}

// Validate checks that the config is usable.
func (c *Config) Validate() error {
	if c.DataDir == "" {
		return fmt.Errorf("data_dir is required")
	}
	if c.WSPort < 1 || c.WSPort > 65535 {
		return fmt.Errorf("ws_port must be 1-65535, got %d", c.WSPort)
	}
	if c.APIPort < 1 || c.APIPort > 65535 {
		return fmt.Errorf("api_port must be 1-65535, got %d", c.APIPort)
	}
	if c.SIPEnabled {
		if c.SIPDomain == "" {
			return fmt.Errorf("sip_domain is required when SIP is enabled")
		}
	}
	return nil
}

// EnsureDataDir creates the data directory and subdirectories if needed.
func (c *Config) EnsureDataDir() error {
	dirs := []string{
		c.DataDir,
		filepath.Join(c.DataDir, "keys"),
		filepath.Join(c.DataDir, "db"),
		filepath.Join(c.DataDir, "logs"),
		filepath.Join(c.DataDir, "mailbox"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0700); err != nil {
			return fmt.Errorf("create dir %s: %w", d, err)
		}
	}
	return nil
}

// DBPath returns the path to the SQLite database.
func (c *Config) DBPath() string {
	return filepath.Join(c.DataDir, "db", "comms.db")
}

// KeyDir returns the path to the keys directory.
func (c *Config) KeyDir() string {
	return filepath.Join(c.DataDir, "keys")
}

// ConfigPath returns the path to the YAML config file.
func (c *Config) ConfigPath() string {
	return filepath.Join(c.DataDir, "config.yaml")
}

// Save writes the config to the YAML file in the data directory.
func (c *Config) Save() error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	path := c.ConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	return os.WriteFile(path, data, 0600)
}
