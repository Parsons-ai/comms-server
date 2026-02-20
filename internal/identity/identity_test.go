package identity

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGenerate(t *testing.T) {
	id, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(id.PublicKey) != 32 {
		t.Errorf("public key length: got %d, want 32", len(id.PublicKey))
	}
	if len(id.PrivateKey) != 64 {
		t.Errorf("private key length: got %d, want 64", len(id.PrivateKey))
	}
}

func TestPublicKeyHex(t *testing.T) {
	id, _ := Generate()
	hex := id.PublicKeyHex()
	if len(hex) != 64 {
		t.Errorf("hex length: got %d, want 64", len(hex))
	}
}

func TestShortID(t *testing.T) {
	id, _ := Generate()
	short := id.ShortID()
	if len(short) != 16 {
		t.Errorf("short ID length: got %d, want 16", len(short))
	}
}

func TestSignAndVerify(t *testing.T) {
	id, _ := Generate()
	msg := []byte("hello COMMS")

	sig := id.Sign(msg)
	if len(sig) != 64 {
		t.Errorf("signature length: got %d, want 64", len(sig))
	}

	if !Verify(id.PublicKey, msg, sig) {
		t.Error("valid signature rejected")
	}

	// Tamper with message
	if Verify(id.PublicKey, []byte("tampered"), sig) {
		t.Error("tampered message accepted")
	}

	// Wrong key
	other, _ := Generate()
	if Verify(other.PublicKey, msg, sig) {
		t.Error("wrong key accepted")
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()

	// Generate and save
	id, _ := Generate()
	if err := id.Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Verify files exist
	if _, err := os.Stat(filepath.Join(dir, "server.key")); err != nil {
		t.Errorf("private key file not found: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "server.pub")); err != nil {
		t.Errorf("public key file not found: %v", err)
	}

	// Load
	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if id.PublicKeyHex() != loaded.PublicKeyHex() {
		t.Error("public keys don't match after load")
	}

	// Verify loaded key can sign/verify
	msg := []byte("persistence test")
	sig := loaded.Sign(msg)
	if !Verify(loaded.PublicKey, msg, sig) {
		t.Error("loaded key can't sign correctly")
	}
}

func TestLoadOrGenerate(t *testing.T) {
	dir := t.TempDir()

	// First call should generate
	id1, isNew, err := LoadOrGenerate(dir)
	if err != nil {
		t.Fatalf("LoadOrGenerate (first): %v", err)
	}
	if !isNew {
		t.Error("expected isNew=true on first call")
	}

	// Second call should load
	id2, isNew, err := LoadOrGenerate(dir)
	if err != nil {
		t.Fatalf("LoadOrGenerate (second): %v", err)
	}
	if isNew {
		t.Error("expected isNew=false on second call")
	}

	if id1.PublicKeyHex() != id2.PublicKeyHex() {
		t.Error("loaded identity doesn't match generated identity")
	}
}

func TestPublicKeyFromHex(t *testing.T) {
	id, _ := Generate()
	hex := id.PublicKeyHex()

	parsed, err := PublicKeyFromHex(hex)
	if err != nil {
		t.Fatalf("PublicKeyFromHex: %v", err)
	}

	if id.PublicKeyHex() != (&Identity{PublicKey: parsed}).PublicKeyHex() {
		t.Error("parsed key doesn't match original")
	}

	// Invalid hex
	if _, err := PublicKeyFromHex("invalid"); err == nil {
		t.Error("expected error for invalid hex")
	}

	// Wrong length
	if _, err := PublicKeyFromHex("aabb"); err == nil {
		t.Error("expected error for wrong length")
	}
}
