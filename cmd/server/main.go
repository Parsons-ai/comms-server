package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"time"

	"github.com/Parsons-ai/comms-server/internal/api"
	"github.com/Parsons-ai/comms-server/internal/cleanup"
	"github.com/Parsons-ai/comms-server/internal/config"
	"github.com/Parsons-ai/comms-server/internal/dashboard"
	"github.com/Parsons-ai/comms-server/internal/server"
	"github.com/Parsons-ai/comms-server/internal/signaling"
	"github.com/Parsons-ai/comms-server/internal/sip"
)

var version = "0.1.0"

func main() {
	// CLI subcommands
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version":
			fmt.Printf("COMMS Server v%s\n", version)
			os.Exit(0)
		case "setup":
			runSetup()
			return
		case "status":
			runStatus()
			return
		}
	}

	// Default: run the server
	runServer()
}

func runServer() {
	// CLI flags
	configFile := flag.String("config", "", "path to config file (default: ~/.comms/config.yaml)")
	dataDir := flag.String("data-dir", "", "data directory (overrides config)")
	wsPort := flag.Int("ws-port", 0, "WebSocket signaling port (overrides config)")
	apiPort := flag.Int("api-port", 0, "REST API port (overrides config)")
	logLevel := flag.String("log-level", "", "log level: debug, info, warn, error")
	flag.Parse()

	// Load config: defaults -> file -> env -> CLI
	var cfg *config.Config
	var err error

	if *configFile != "" {
		cfg, err = config.LoadFromFile(*configFile)
	} else {
		// Try default location
		defaultCfg := config.Default()
		cfg, err = config.LoadFromFile(defaultCfg.ConfigPath())
	}
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	cfg.ApplyEnv()

	// CLI overrides
	if *dataDir != "" {
		cfg.DataDir = *dataDir
	}
	if *wsPort != 0 {
		cfg.WSPort = *wsPort
	}
	if *apiPort != 0 {
		cfg.APIPort = *apiPort
	}
	if *logLevel != "" {
		cfg.LogLevel = *logLevel
	}

	if err := cfg.Validate(); err != nil {
		log.Fatalf("invalid config: %v", err)
	}

	// Create server
	app, err := server.New(cfg)
	if err != nil {
		log.Fatalf("create server: %v", err)
	}

	// Register services in startup order

	// 1. Signaling server (WebSocket for WebRTC setup)
	sigServer := signaling.New(
		fmt.Sprintf("%s:%d", cfg.ListenAddr, cfg.WSPort),
		app.DB,
		app.Logger,
	)
	app.Register(sigServer)

	// 2. REST API server
	apiServer := api.New(
		fmt.Sprintf("%s:%d", cfg.ListenAddr, cfg.APIPort),
		app.DB,
		app.Identity,
		app.Logger,
	)
	app.Register(apiServer)

	// 3. Dashboard (mounted on API server's mux)
	dash := dashboard.New(app.DB, app.Identity, version, fmt.Sprintf("%d", cfg.APIPort), app.Logger)
	dash.Mount(apiServer.Mux())

	// 4. SIP/VoIP bridge (optional, for legacy phone integration)
	if cfg.SIPEnabled {
		sipCfg := sip.Config{
			Provider: sip.Provider(cfg.SIPProvider),
			Domain:   cfg.SIPDomain,
			Username: cfg.SIPUsername,
			Password: cfg.SIPPassword,
			APIKey:   os.Getenv("COMMS_SIP_API_KEY"),
		}
		sipBridge := sip.New(
			fmt.Sprintf("%s:%d", cfg.ListenAddr, cfg.SIPPort),
			app.DB,
			sipCfg,
			app.Logger,
		)
		app.Register(sipBridge)
	}

	// 5. Cleanup service (message expiry, storage tracking)
	cleanupSvc := cleanup.New(app.DB, 5*time.Minute, app.Logger)
	app.Register(cleanupSvc)

	fmt.Printf("COMMS Server v%s\n", version)
	fmt.Printf("Public Key: %s\n", app.Identity.PublicKeyHex())
	fmt.Printf("Data Dir:   %s\n", cfg.DataDir)
	fmt.Printf("API:        http://%s:%d\n", cfg.ListenAddr, cfg.APIPort)
	fmt.Printf("Dashboard:  http://%s:%d/dashboard\n", cfg.ListenAddr, cfg.APIPort)
	fmt.Printf("Signaling:  ws://%s:%d/ws\n", cfg.ListenAddr, cfg.WSPort)
	if cfg.SIPEnabled {
		fmt.Printf("SIP:        http://%s:%d\n", cfg.ListenAddr, cfg.SIPPort)
	}
	fmt.Println()

	if err := app.Run(); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func runSetup() {
	fmt.Println("COMMS Server Setup")
	fmt.Println("==================")

	cfg := config.Default()
	if err := cfg.EnsureDataDir(); err != nil {
		log.Fatalf("create data dir: %v", err)
	}

	fmt.Printf("Data directory: %s\n", cfg.DataDir)

	// Save default config
	if err := cfg.Save(); err != nil {
		log.Fatalf("save config: %v", err)
	}
	fmt.Printf("Config saved: %s\n", cfg.ConfigPath())

	fmt.Println("\nSetup complete. Run 'comms-server' to start.")
}

func runStatus() {
	cfg := config.Default()
	cfg.ApplyEnv()

	fmt.Printf("COMMS Server v%s\n", version)
	fmt.Printf("Data Dir:  %s\n", cfg.DataDir)
	fmt.Printf("API Port:  %d\n", cfg.APIPort)
	fmt.Printf("WS Port:   %d\n", cfg.WSPort)

	// Check if keys exist
	keyPath := cfg.KeyDir() + "/server.key"
	if _, err := os.Stat(keyPath); err == nil {
		fmt.Println("Identity:  configured")
	} else {
		fmt.Println("Identity:  not configured (run setup or start server)")
	}

	// Check if DB exists
	if _, err := os.Stat(cfg.DBPath()); err == nil {
		fmt.Println("Database:  exists")
	} else {
		fmt.Println("Database:  not created (will be created on first start)")
	}
}
