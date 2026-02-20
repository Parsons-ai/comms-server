package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// Identity represents a COMMS node's Ed25519 keypair.
// The public key IS the node's address on the network.
type Identity struct {
	PublicKey  ed25519.PublicKey
	PrivateKey ed25519.PrivateKey
}

// Generate creates a new random Ed25519 keypair.
func Generate() (*Identity, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate keypair: %w", err)
	}
	return &Identity{PublicKey: pub, PrivateKey: priv}, nil
}

// PublicKeyHex returns the public key as a hex string (the node's address).
func (id *Identity) PublicKeyHex() string {
	return hex.EncodeToString(id.PublicKey)
}

// ShortID returns the first 16 hex chars of the public key for display.
func (id *Identity) ShortID() string {
	h := id.PublicKeyHex()
	if len(h) > 16 {
		return h[:16]
	}
	return h
}

// Sign signs a message with the private key.
func (id *Identity) Sign(message []byte) []byte {
	return ed25519.Sign(id.PrivateKey, message)
}

// Verify checks a signature against a public key.
func Verify(publicKey ed25519.PublicKey, message, sig []byte) bool {
	return ed25519.Verify(publicKey, message, sig)
}

// Save persists the keypair to disk.
// Private key is stored as raw bytes (64 bytes).
// Public key is stored as raw bytes (32 bytes).
func (id *Identity) Save(keyDir string) error {
	privPath := filepath.Join(keyDir, "server.key")
	pubPath := filepath.Join(keyDir, "server.pub")

	if err := os.WriteFile(privPath, id.PrivateKey, 0600); err != nil {
		return fmt.Errorf("write private key: %w", err)
	}
	if err := os.WriteFile(pubPath, id.PublicKey, 0644); err != nil {
		return fmt.Errorf("write public key: %w", err)
	}
	return nil
}

// Load reads a keypair from disk.
func Load(keyDir string) (*Identity, error) {
	privPath := filepath.Join(keyDir, "server.key")
	pubPath := filepath.Join(keyDir, "server.pub")

	privBytes, err := os.ReadFile(privPath)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}
	pubBytes, err := os.ReadFile(pubPath)
	if err != nil {
		return nil, fmt.Errorf("read public key: %w", err)
	}

	if len(privBytes) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid private key size: got %d, want %d", len(privBytes), ed25519.PrivateKeySize)
	}
	if len(pubBytes) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid public key size: got %d, want %d", len(pubBytes), ed25519.PublicKeySize)
	}

	return &Identity{
		PublicKey:  ed25519.PublicKey(pubBytes),
		PrivateKey: ed25519.PrivateKey(privBytes),
	}, nil
}

// LoadOrGenerate attempts to load an existing keypair, or generates a new one.
func LoadOrGenerate(keyDir string) (*Identity, bool, error) {
	id, err := Load(keyDir)
	if err == nil {
		return id, false, nil
	}

	// Generate new identity
	id, err = Generate()
	if err != nil {
		return nil, false, err
	}

	if err := id.Save(keyDir); err != nil {
		return nil, false, err
	}
	return id, true, nil
}

// PublicKeyFromHex parses a hex-encoded public key.
func PublicKeyFromHex(h string) (ed25519.PublicKey, error) {
	b, err := hex.DecodeString(h)
	if err != nil {
		return nil, fmt.Errorf("decode hex: %w", err)
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid public key size: got %d, want %d", len(b), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(b), nil
}
