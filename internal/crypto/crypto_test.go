package crypto

import (
	"bytes"
	"testing"
)

func TestSealOpenRoundTrip(t *testing.T) {
	key, err := NewMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte("the quick brown fox jumps over the lazy dog")
	ct, err := Seal(key, plaintext, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(ct, plaintext) {
		t.Fatal("ciphertext equals plaintext")
	}
	pt, err := Open(key, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pt, plaintext) {
		t.Fatalf("round trip mismatch: got %q", pt)
	}
}

func TestSealNonceRandomized(t *testing.T) {
	key, _ := NewMasterKey()
	pt := []byte("same plaintext")
	a, _ := Seal(key, pt, nil)
	b, _ := Seal(key, pt, nil)
	if bytes.Equal(a, b) {
		t.Fatal("two seals of the same plaintext were identical (nonce reuse?)")
	}
}

func TestOpenWrongKeyFails(t *testing.T) {
	k1, _ := NewMasterKey()
	k2, _ := NewMasterKey()
	ct, _ := Seal(k1, []byte("secret"), nil)
	if _, err := Open(k2, ct, nil); err == nil {
		t.Fatal("expected decryption failure with wrong key")
	}
}

func TestOpenTamperedFails(t *testing.T) {
	key, _ := NewMasterKey()
	ct, _ := Seal(key, []byte("secret data"), nil)
	ct[len(ct)-1] ^= 0xFF // flip a tag bit
	if _, err := Open(key, ct, nil); err == nil {
		t.Fatal("expected authentication failure on tampered ciphertext")
	}
}

func TestDeriveKEKDeterministic(t *testing.T) {
	salt, _ := NewSalt()
	p := DefaultKDFParams()
	a := DeriveKEK([]byte("pw"), salt, p)
	b := DeriveKEK([]byte("pw"), salt, p)
	if !bytes.Equal(a, b) {
		t.Fatal("KEK derivation is not deterministic")
	}
	if len(a) != KeySize {
		t.Fatalf("KEK size = %d, want %d", len(a), KeySize)
	}
	c := DeriveKEK([]byte("different"), salt, p)
	if bytes.Equal(a, c) {
		t.Fatal("different passwords produced the same KEK")
	}
}

func TestDeriveSubKeyDistinct(t *testing.T) {
	mk, _ := NewMasterKey()
	a := DeriveSubKey(mk, "context-a")
	b := DeriveSubKey(mk, "context-b")
	if bytes.Equal(a, b) {
		t.Fatal("different contexts produced the same subkey")
	}
	if !bytes.Equal(a, DeriveSubKey(mk, "context-a")) {
		t.Fatal("subkey derivation not deterministic")
	}
}

func TestHashChunkStable(t *testing.T) {
	data := []byte("hash me")
	if HashChunk(data) != HashChunk(data) {
		t.Fatal("hash not stable")
	}
	if HashChunk(data) == HashChunk([]byte("hash me!")) {
		t.Fatal("distinct inputs hashed equal")
	}
}
