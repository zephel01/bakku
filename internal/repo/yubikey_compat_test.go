package repo

import (
	"bytes"
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/zephel01/bakku/internal/crypto"
	"github.com/zephel01/bakku/internal/yubikey"
)

// legacyYubiKeySlot builds a keyFile the way pre-v0.2.4 bakku did: the KEK is
// derived with the legacy BLAKE3 path and NO "kdf" marker is stored in Params.
func legacyYubiKeySlot(t *testing.T, response, master []byte) keyFile {
	t.Helper()
	salt, err := crypto.NewSalt()
	if err != nil {
		t.Fatal(err)
	}
	kek := deriveYubiKEKLegacy(response, salt)
	wrapped, err := crypto.Seal(kek, master, nil)
	if err != nil {
		t.Fatal(err)
	}
	return keyFile{
		Version: repoVersion,
		Type:    KeyTypeYubiKey,
		Created: time.Now().UTC(),
		Salt:    hex.EncodeToString(salt),
		Data:    hex.EncodeToString(wrapped),
		ID:      "legacyslot",
		Params: map[string]any{ // note: no "kdf" key -> legacy derivation
			"slot":      2,
			"challenge": hex.EncodeToString([]byte("chal")),
		},
	}
}

func mockResponse(t *testing.T, secret, challenge []byte) []byte {
	t.Helper()
	m := &yubikey.MockResponder{Secret: secret}
	resp, err := m.Respond(context.Background(), 2, challenge)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, len(resp))
	copy(out, resp[:])
	return out
}

// TestYubiKeyLegacySlotStillUnlocks verifies backward compatibility: a slot
// written by an older bakku (legacy BLAKE3 KEK, no "kdf" param) still unlocks
// via the legacy derivation path after the HKDF change.
func TestYubiKeyLegacySlotStillUnlocks(t *testing.T) {
	master, err := crypto.NewMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	response := mockResponse(t, []byte("device-secret"), []byte("chal"))

	kf := legacyYubiKeySlot(t, response, master)

	got, err := (yubiKeyProvider{}).UnlockSlot(kf, response)
	if err != nil {
		t.Fatalf("legacy slot failed to unlock: %v", err)
	}
	if !bytes.Equal(got, master) {
		t.Fatal("legacy slot unlocked but returned wrong master key")
	}
}

// TestYubiKeyNewSlotUsesHKDF verifies new slots are marked hkdf-sha256 and that
// HKDF and legacy derivations genuinely differ for the same inputs.
func TestYubiKeyNewSlotUsesHKDF(t *testing.T) {
	master, err := crypto.NewMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	response := mockResponse(t, []byte("device-secret"), []byte("chal"))

	kf, err := buildYubiKeySlot(yubiKeyCredential{Slot: 2, Challenge: []byte("chal"), Response: response}, master)
	if err != nil {
		t.Fatal(err)
	}
	if kdf, _ := kf.Params["kdf"].(string); kdf != yubiKeyKDFv2 {
		t.Fatalf("new slot kdf param = %q, want %q", kf.Params["kdf"], yubiKeyKDFv2)
	}
	got, err := (yubiKeyProvider{}).UnlockSlot(kf, response)
	if err != nil || !bytes.Equal(got, master) {
		t.Fatalf("hkdf slot round-trip failed: err=%v equal=%v", err, bytes.Equal(got, master))
	}

	salt := []byte("0123456789abcdef")
	if bytes.Equal(deriveYubiKEKHKDF(response, salt), deriveYubiKEKLegacy(response, salt)) {
		t.Fatal("HKDF and legacy derivations produced the same KEK; they must differ")
	}
}
