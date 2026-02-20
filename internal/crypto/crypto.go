package crypto

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha512"
	"errors"
	"fmt"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/nacl/box"
)

const (
	// NonceSize is the NaCl nonce size (24 bytes).
	NonceSize = 24
	// Overhead is the NaCl box MAC overhead (16 bytes).
	Overhead = box.Overhead
)

// DeriveX25519Keypair converts an Ed25519 private key into an X25519 keypair.
// The X25519 public key is derived via scalar multiplication of the private
// key with the Curve25519 basepoint. Users exchange X25519 public keys
// alongside their Ed25519 public keys for end-to-end encryption.
func DeriveX25519Keypair(edPriv ed25519.PrivateKey) (pub, priv [32]byte, err error) {
	// Hash the Ed25519 seed with SHA-512, take first 32 bytes, clamp
	seed := edPriv.Seed()
	h := sha512.Sum512(seed)
	copy(priv[:], h[:32])
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64

	// Derive X25519 public key from the private key
	pubSlice, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return [32]byte{}, [32]byte{}, fmt.Errorf("derive X25519 public key: %w", err)
	}
	copy(pub[:], pubSlice)
	return pub, priv, nil
}

// Encrypt encrypts plaintext using NaCl box (X25519 + XSalsa20-Poly1305).
// Returns: nonce (24 bytes) || ciphertext (with 16-byte MAC appended).
func Encrypt(plaintext []byte, senderPriv, recipientPub [32]byte) ([]byte, error) {
	var nonce [NonceSize]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	// Seal appends the encrypted+authenticated message to the nonce prefix
	out := box.Seal(nonce[:], plaintext, &nonce, &recipientPub, &senderPriv)
	return out, nil
}

// Decrypt decrypts ciphertext encrypted with NaCl box.
// Expects: nonce (24 bytes) || ciphertext (with 16-byte MAC).
func Decrypt(encrypted []byte, senderPub, recipientPriv [32]byte) ([]byte, error) {
	if len(encrypted) < NonceSize+Overhead {
		return nil, errors.New("ciphertext too short")
	}
	var nonce [NonceSize]byte
	copy(nonce[:], encrypted[:NonceSize])

	plaintext, ok := box.Open(nil, encrypted[NonceSize:], &nonce, &senderPub, &recipientPriv)
	if !ok {
		return nil, errors.New("decryption failed: authentication error")
	}
	return plaintext, nil
}

// EncryptForRecipient is a convenience function that takes the sender's
// Ed25519 private key and the recipient's X25519 public key.
func EncryptForRecipient(plaintext []byte, senderEdPriv ed25519.PrivateKey, recipientX25519Pub [32]byte) ([]byte, error) {
	_, senderX25519Priv, err := DeriveX25519Keypair(senderEdPriv)
	if err != nil {
		return nil, err
	}
	return Encrypt(plaintext, senderX25519Priv, recipientX25519Pub)
}

// DecryptFromSender is a convenience function that takes the recipient's
// Ed25519 private key and the sender's X25519 public key.
func DecryptFromSender(encrypted []byte, senderX25519Pub [32]byte, recipientEdPriv ed25519.PrivateKey) ([]byte, error) {
	_, recipientX25519Priv, err := DeriveX25519Keypair(recipientEdPriv)
	if err != nil {
		return nil, err
	}
	return Decrypt(encrypted, senderX25519Pub, recipientX25519Priv)
}
