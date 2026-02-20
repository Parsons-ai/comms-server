package crypto

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
)

func generateTestKeypair(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return priv
}

func TestDeriveX25519Keypair(t *testing.T) {
	priv := generateTestKeypair(t)

	pub, x25519Priv, err := DeriveX25519Keypair(priv)
	if err != nil {
		t.Fatalf("derive keypair: %v", err)
	}

	// Keys should be non-zero
	if pub == [32]byte{} {
		t.Error("X25519 public key is all zeros")
	}
	if x25519Priv == [32]byte{} {
		t.Error("X25519 private key is all zeros")
	}

	// Deriving again should produce the same keys (deterministic)
	pub2, priv2, _ := DeriveX25519Keypair(priv)
	if pub != pub2 {
		t.Error("X25519 public key not deterministic")
	}
	if priv2 != x25519Priv {
		t.Error("X25519 private key not deterministic")
	}
}

func TestEncryptDecrypt(t *testing.T) {
	aliceEd := generateTestKeypair(t)
	bobEd := generateTestKeypair(t)

	alicePub, alicePriv, _ := DeriveX25519Keypair(aliceEd)
	bobPub, bobPriv, _ := DeriveX25519Keypair(bobEd)

	plaintext := []byte("hello, this is a secret message from Alice to Bob")

	// Alice encrypts for Bob
	encrypted, err := Encrypt(plaintext, alicePriv, bobPub)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// Encrypted should be longer than plaintext (nonce + MAC)
	if len(encrypted) != len(plaintext)+NonceSize+Overhead {
		t.Errorf("encrypted length: got %d, want %d", len(encrypted), len(plaintext)+NonceSize+Overhead)
	}

	// Bob decrypts
	decrypted, err := Decrypt(encrypted, alicePub, bobPriv)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}

	if !bytes.Equal(plaintext, decrypted) {
		t.Errorf("decrypted mismatch: got %q, want %q", decrypted, plaintext)
	}
}

func TestDecryptWrongKey(t *testing.T) {
	aliceEd := generateTestKeypair(t)
	bobEd := generateTestKeypair(t)
	eveEd := generateTestKeypair(t)

	_, alicePriv, _ := DeriveX25519Keypair(aliceEd)
	bobPub, _, _ := DeriveX25519Keypair(bobEd)
	evePub, evePriv, _ := DeriveX25519Keypair(eveEd)
	_ = evePub

	plaintext := []byte("secret message")

	// Alice encrypts for Bob
	encrypted, _ := Encrypt(plaintext, alicePriv, bobPub)

	// Eve tries to decrypt (should fail)
	_, err := Decrypt(encrypted, evePub, evePriv)
	if err == nil {
		t.Error("expected decryption to fail with wrong key")
	}
}

func TestDecryptTampered(t *testing.T) {
	aliceEd := generateTestKeypair(t)
	bobEd := generateTestKeypair(t)

	alicePub, alicePriv, _ := DeriveX25519Keypair(aliceEd)
	bobPub, bobPriv, _ := DeriveX25519Keypair(bobEd)

	encrypted, _ := Encrypt([]byte("message"), alicePriv, bobPub)

	// Tamper with the ciphertext
	encrypted[len(encrypted)-1] ^= 0xff

	_, err := Decrypt(encrypted, alicePub, bobPriv)
	if err == nil {
		t.Error("expected decryption to fail on tampered ciphertext")
	}
}

func TestDecryptTooShort(t *testing.T) {
	_, err := Decrypt([]byte("short"), [32]byte{}, [32]byte{})
	if err == nil {
		t.Error("expected error for short ciphertext")
	}
}

func TestConvenienceFunctions(t *testing.T) {
	aliceEd := generateTestKeypair(t)
	bobEd := generateTestKeypair(t)

	// Get Bob's X25519 public key (would normally be exchanged out-of-band)
	bobX25519Pub, _, _ := DeriveX25519Keypair(bobEd)

	// Alice's X25519 public key
	aliceX25519Pub, _, _ := DeriveX25519Keypair(aliceEd)

	plaintext := []byte("convenience function test message")

	// Alice encrypts using convenience function
	encrypted, err := EncryptForRecipient(plaintext, aliceEd, bobX25519Pub)
	if err != nil {
		t.Fatalf("EncryptForRecipient: %v", err)
	}

	// Bob decrypts using convenience function
	decrypted, err := DecryptFromSender(encrypted, aliceX25519Pub, bobEd)
	if err != nil {
		t.Fatalf("DecryptFromSender: %v", err)
	}

	if !bytes.Equal(plaintext, decrypted) {
		t.Errorf("decrypted mismatch: got %q, want %q", decrypted, plaintext)
	}
}

func TestEmptyPlaintext(t *testing.T) {
	aliceEd := generateTestKeypair(t)
	bobEd := generateTestKeypair(t)

	_, alicePriv, _ := DeriveX25519Keypair(aliceEd)
	alicePub, _, _ := DeriveX25519Keypair(aliceEd)
	bobPub, bobPriv, _ := DeriveX25519Keypair(bobEd)

	// Encrypt empty message
	encrypted, err := Encrypt([]byte{}, alicePriv, bobPub)
	if err != nil {
		t.Fatalf("encrypt empty: %v", err)
	}

	decrypted, err := Decrypt(encrypted, alicePub, bobPriv)
	if err != nil {
		t.Fatalf("decrypt empty: %v", err)
	}

	if len(decrypted) != 0 {
		t.Errorf("expected empty plaintext, got %d bytes", len(decrypted))
	}
}
