// Package crypto provides the cryptographic primitives used by bakku:
// argon2id key derivation, AES-256-GCM AEAD for blobs, and BLAKE3-based
// key derivation and content hashing.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"

	"github.com/zeebo/blake3"
	"golang.org/x/crypto/argon2"
)

const (
	// KeySize is the size of the master key and all derived keys.
	KeySize = 32
	// nonceSize is the AES-GCM nonce size in bytes.
	nonceSize = 12
	// SaltSize is the size of the argon2id salt.
	SaltSize = 16
)

// KDFParams holds argon2id parameters. They are stored (with the salt) in the
// key file so the same KEK can be re-derived from the password on unlock.
type KDFParams struct {
	Time    uint32 `json:"time"`
	Memory  uint32 `json:"memory"` // in KiB
	Threads uint8  `json:"threads"`
}

// DefaultKDFParams returns reasonable interactive argon2id parameters.
func DefaultKDFParams() KDFParams {
	return KDFParams{
		Time:    3,
		Memory:  64 * 1024, // 64 MiB
		Threads: 4,
	}
}

// DeriveKEK derives a 32-byte key-encryption-key from a password using argon2id.
func DeriveKEK(password []byte, salt []byte, p KDFParams) []byte {
	return argon2.IDKey(password, salt, p.Time, p.Memory, p.Threads, KeySize)
}

// NewSalt returns a fresh random salt of SaltSize bytes.
func NewSalt() ([]byte, error) {
	return randomBytes(SaltSize)
}

// NewMasterKey generates a fresh random 32-byte master key.
func NewMasterKey() ([]byte, error) {
	return randomBytes(KeySize)
}

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return nil, err
	}
	return b, nil
}

// Wipe overwrites b with zeros. It is used to clear transient key material
// (KEKs, derived subkeys, master keys, passwords) from memory as soon as it is
// no longer needed, reducing the window in which secrets could leak via a core
// dump or swap. This is best-effort defense in depth: Go's garbage collector
// may still have moved copies, and immutable strings cannot be wiped, so
// callers should prefer []byte for secrets. Safe to call on a nil slice.
func Wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// DeriveSubKey derives a purpose-specific 32-byte subkey from the master key
// using BLAKE3's key-derivation mode with the given context string. The context
// must be a fixed, application-unique, hard-coded string per purpose.
func DeriveSubKey(masterKey []byte, context string) []byte {
	out := make([]byte, KeySize)
	blake3.DeriveKey(context, masterKey, out)
	return out
}

// HashChunk returns the BLAKE3-256 digest of the plaintext chunk. This is the
// content-addressable blob ID.
func HashChunk(data []byte) [32]byte {
	return blake3.Sum256(data)
}

// Seal encrypts plaintext with AES-256-GCM using a fresh random nonce. The
// returned ciphertext is nonce || gcm-ciphertext-and-tag. aad may be nil.
func Seal(key, plaintext, aad []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce, err := randomBytes(nonceSize)
	if err != nil {
		return nil, err
	}
	// Prepend the nonce; Seal appends ciphertext+tag to the dst slice.
	out := make([]byte, nonceSize, nonceSize+len(plaintext)+gcm.Overhead())
	copy(out, nonce)
	return gcm.Seal(out, nonce, plaintext, aad), nil
}

// Open decrypts data produced by Seal. data must be nonce || ciphertext-and-tag.
func Open(key, data, aad []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(data) < nonceSize {
		return nil, errors.New("crypto: ciphertext too short")
	}
	nonce := data[:nonceSize]
	ct := data[nonceSize:]
	pt, err := gcm.Open(nil, nonce, ct, aad)
	if err != nil {
		return nil, fmt.Errorf("crypto: decryption failed: %w", err)
	}
	return pt, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("crypto: key must be %d bytes, got %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
