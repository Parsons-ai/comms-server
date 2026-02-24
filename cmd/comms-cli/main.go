// comms-cli: Programmatic test client for the COMMS server.
//
// Reuses server packages directly (identity, signaling, crypto) —
// zero reimplementation needed.
//
// Usage:
//
//	comms-cli [--server <url>] <command> [args]
//	go build ./cmd/comms-cli && ./comms-cli status
package main

import (
	"fmt"
	"os"
	"strings"
)

const defaultServer = "http://localhost:8080"

func main() {
	args := os.Args[1:]

	// Parse --server flag anywhere in the args
	server := defaultServer
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--server" {
			server = args[i+1]
			args = append(args[:i], args[i+2:]...)
			break
		}
	}

	if len(args) == 0 {
		printUsage()
		os.Exit(1)
	}

	cmd := args[0]
	args = args[1:]

	var err error
	switch cmd {
	case "status":
		err = cmdStatus(server)

	case "register":
		if len(args) < 2 {
			fatalf("usage: comms-cli register <name> <display-name>")
		}
		err = cmdRegister(server, args[0], strings.Join(args[1:], " "))

	case "listen":
		if len(args) < 1 {
			fatalf("usage: comms-cli listen <profile>")
		}
		err = cmdListen(server, args[0])

	// ─── Info commands ───────────────────────────────────────────────────────
	case "whoami":
		if len(args) < 1 {
			fatalf("usage: comms-cli whoami <profile>")
		}
		err = cmdWhoami(server, args[0])

	case "contacts":
		if len(args) < 1 {
			fatalf("usage: comms-cli contacts <profile>")
		}
		err = cmdContacts(server, args[0])

	case "calls":
		if len(args) < 1 {
			fatalf("usage: comms-cli calls <profile>")
		}
		err = cmdCalls(server, args[0])

	case "dids":
		if len(args) < 1 {
			fatalf("usage: comms-cli dids <profile>")
		}
		err = cmdDIDs(server, args[0])

	// ─── SMS commands ────────────────────────────────────────────────────────
	case "dm":
		if len(args) < 3 {
			fatalf("usage: comms-cli dm <profile> <to-pubkey> <text...>")
		}
		err = cmdDMSend(server, args[0], args[1], strings.Join(args[2:], " "))

	case "sms":
		if len(args) < 1 {
			fatalf("usage: comms-cli sms <send|list|conversations> ...")
		}
		sub := args[0]
		args = args[1:]
		switch sub {
		case "send":
			if len(args) < 3 {
				fatalf("usage: comms-cli sms send <profile> <to> <text...>")
			}
			err = cmdSMSSend(server, args[0], args[1], strings.Join(args[2:], " "))
		case "list":
			if len(args) < 2 {
				fatalf("usage: comms-cli sms list <profile> <phone>")
			}
			err = cmdSMSList(server, args[0], args[1])
		case "conversations":
			if len(args) < 1 {
				fatalf("usage: comms-cli sms conversations <profile>")
			}
			err = cmdSMSConversations(server, args[0])
		default:
			fatalf("unknown sms subcommand: %s", sub)
		}

	// ─── Audio call commands ─────────────────────────────────────────────────
	case "dial":
		if len(args) < 2 {
			fatalf("usage: comms-cli dial <profile> <phone> [--tts \"text\"] [--audio file.wav] [--record out.wav] [--stt]")
		}
		profile := args[0]
		phone := args[1]
		audioCfg, err2 := parseAudioFlags(args[2:])
		if err2 != nil {
			fatalf("%v", err2)
		}
		err = cmdDial(server, profile, phone, audioCfg)

	case "answer":
		if len(args) < 1 {
			fatalf("usage: comms-cli answer <profile> [--tts \"text\"] [--audio file.wav] [--record out.wav] [--stt]")
		}
		profile := args[0]
		audioCfg, err2 := parseAudioFlags(args[1:])
		if err2 != nil {
			fatalf("%v", err2)
		}
		err = cmdAnswer(server, profile, audioCfg)

	case "transcribe":
		if len(args) < 1 {
			fatalf("usage: comms-cli transcribe <file.wav>")
		}
		err = cmdTranscribe(server, args[0])

	// ─── Legacy commands (no WebRTC audio) ───────────────────────────────────
	case "call":
		if len(args) < 2 {
			fatalf("usage: comms-cli call <profile> <phone>  (use 'dial' for WebRTC audio)")
		}
		err = cmdCall(server, args[0], args[1])

	case "hangup":
		if len(args) < 2 {
			fatalf("usage: comms-cli hangup <profile> <call-id>")
		}
		err = cmdHangup(server, args[0], args[1])

	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
		printUsage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// parseAudioFlags extracts audio-related flags from an args slice.
// Returns an AudioConfig and any unrecognised args (which are ignored).
func parseAudioFlags(args []string) (AudioConfig, error) {
	var cfg AudioConfig
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--tts":
			if i+1 >= len(args) {
				return cfg, fmt.Errorf("--tts requires an argument")
			}
			cfg.TTSText = args[i+1]
			i++
		case "--audio":
			if i+1 >= len(args) {
				return cfg, fmt.Errorf("--audio requires a file path")
			}
			cfg.AudioFile = args[i+1]
			i++
		case "--record":
			if i+1 >= len(args) {
				return cfg, fmt.Errorf("--record requires a file path")
			}
			cfg.RecordTo = args[i+1]
			i++
		case "--stt":
			cfg.AutoSTT = true
		}
	}
	return cfg, nil
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func printUsage() {
	fmt.Fprint(os.Stderr, `comms-cli — COMMS server test client

Usage:
  comms-cli [--server <url>] <command> [args]

Info commands:
  status                                Server health + capabilities
  whoami <profile>                      Show profile identity + DIDs
  contacts <profile>                    List contacts
  calls <profile>                       Recent call log
  dids <profile>                        List server DIDs

Registration:
  register <name> <display-name>        Create user + save profile locally

Messaging:
  listen <profile>                      WebSocket listener (print all messages)
  dm <profile> <pubkey> <text...>       Send COMMS native direct message (free, E2E)
  sms send <profile> <to> <text...>     Send SMS
  sms list <profile> <phone>            Message history with a number
  sms conversations <profile>           All SMS conversations

Audio calls (WebRTC):
  dial <profile> <phone> [flags]        Outbound PSTN call with WebRTC audio
  answer <profile> [flags]              Wait for inbound call, answer with audio
  transcribe <file.wav>                 Transcribe WAV via whisper

  Audio flags:
    --tts "text"        Generate TTS (macOS say) and send as call audio
    --audio file.wav    Send a WAV file as call audio (8kHz mono 16-bit)
    --record out.wav    Record received audio to WAV file
    --stt               Record + auto-transcribe at call end

Legacy call control (no WebRTC audio):
  call <profile> <phone>                Initiate outbound PSTN call
  hangup <profile> <call-id>            Hang up an active call

Options:
  --server <url>   Server URL (default: http://localhost:8080)

Two-terminal call test:
  Terminal 1:  comms-cli answer alice --record /tmp/recv.wav
  Terminal 2:  comms-cli dial bob +14065598011 --tts "hello from bob"
  Transcribe:  comms-cli transcribe /tmp/recv.wav

Profiles are stored in ~/.comms/cli-profiles/<name>/
`)
}
