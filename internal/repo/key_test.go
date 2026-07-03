package repo

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/zephel01/bakku/internal/backend/local"
	"github.com/zephel01/bakku/internal/crypto"
)

// TestKeySlotAddAndOpenWithEither verifies a second password slot can open the
// same repository (the "lose one key, open with another" guarantee).
func TestKeySlotAddAndOpenWithEither(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	be, _ := local.New(dir)
	if _, err := Init(ctx, be, []byte("first-pw")); err != nil {
		t.Fatal(err)
	}

	// Open with first, add a second slot.
	be2, _ := local.New(dir)
	r, err := Open(ctx, be2, []byte("first-pw"))
	if err != nil {
		t.Fatal(err)
	}
	newID, err := r.AddPasswordKeySlot(ctx, []byte("second-pw"))
	if err != nil {
		t.Fatal(err)
	}
	if len(newID) == 0 {
		t.Fatal("expected non-empty new slot id")
	}
	r.Close(ctx)

	// Both passwords must open the repo.
	for _, pw := range []string{"first-pw", "second-pw"} {
		be3, _ := local.New(dir)
		rr, err := Open(ctx, be3, []byte(pw))
		if err != nil {
			t.Fatalf("open with %q failed: %v", pw, err)
		}
		rr.Close(ctx)
	}

	// There should now be two slots.
	be4, _ := local.New(dir)
	r4, _ := Open(ctx, be4, []byte("first-pw"))
	slots, err := r4.ListKeySlots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 2 {
		t.Fatalf("expected 2 slots, got %d", len(slots))
	}
	// Exactly one should be marked Current (the one we opened with).
	var currents int
	for _, s := range slots {
		if s.Current {
			currents++
		}
	}
	if currents != 1 {
		t.Fatalf("expected exactly 1 current slot, got %d", currents)
	}
	r4.Close(ctx)
}

// TestKeySlotRemoveAndRemaining verifies removal works and the remaining slot
// still opens the repository.
func TestKeySlotRemoveAndRemaining(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	be, _ := local.New(dir)
	Init(ctx, be, []byte("first-pw"))

	be2, _ := local.New(dir)
	r, _ := Open(ctx, be2, []byte("first-pw"))
	if _, err := r.AddPasswordKeySlot(ctx, []byte("second-pw")); err != nil {
		t.Fatal(err)
	}
	slots, _ := r.ListKeySlots(ctx)
	// Remove the slot that is NOT current (the second one) to avoid self-removal.
	var toRemove string
	for _, s := range slots {
		if !s.Current {
			toRemove = s.ID
		}
	}
	if toRemove == "" {
		t.Fatal("no non-current slot found")
	}
	removed, wasCurrent, err := r.RemoveKeySlot(ctx, toRemove)
	if err != nil {
		t.Fatal(err)
	}
	if removed != toRemove || wasCurrent {
		t.Fatalf("unexpected remove result: removed=%s wasCurrent=%v", removed, wasCurrent)
	}
	r.Close(ctx)

	// first-pw still opens; second-pw no longer does.
	be3, _ := local.New(dir)
	if _, err := Open(ctx, be3, []byte("first-pw")); err != nil {
		t.Fatalf("first-pw should still open: %v", err)
	}
	be4, _ := local.New(dir)
	if _, err := Open(ctx, be4, []byte("second-pw")); err != ErrWrongPassword {
		t.Fatalf("second-pw should no longer open, got %v", err)
	}
}

// TestKeySlotRefuseLastRemoval verifies the last slot cannot be removed.
func TestKeySlotRefuseLastRemoval(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	be, _ := local.New(dir)
	Init(ctx, be, []byte("only-pw"))

	be2, _ := local.New(dir)
	r, _ := Open(ctx, be2, []byte("only-pw"))
	defer r.Close(ctx)

	slots, _ := r.ListKeySlots(ctx)
	_, _, err := r.RemoveKeySlot(ctx, slots[0].ID)
	if err != ErrLastKeySlot {
		t.Fatalf("expected ErrLastKeySlot, got %v", err)
	}
}

// TestLegacyKeyFileBackwardCompat writes a v0.1.0-style key file (NO "type"
// field) directly and confirms it opens as a "password" slot.
func TestLegacyKeyFileBackwardCompat(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	be, _ := local.New(dir)

	// Build a repository the normal way to get a valid config + master key, then
	// replace its key file with a legacy-format (no "type") equivalent.
	r, err := Init(ctx, be, []byte("legacy-pw"))
	if err != nil {
		t.Fatal(err)
	}
	master := r.master
	r.Close(ctx)

	// Remove the modern key file(s).
	be2, _ := local.New(dir)
	var keyPaths []string
	be2.List(ctx, "keys", func(k string, _ int64) error {
		keyPaths = append(keyPaths, k)
		return nil
	})
	for _, k := range keyPaths {
		if err := be2.Delete(ctx, k); err != nil {
			t.Fatal(err)
		}
	}

	// Craft a legacy key file struct: same fields as keyFile but marshaled from a
	// map with no "type" key, matching what v0.1.0 wrote.
	salt, _ := crypto.NewSalt()
	params := crypto.DefaultKDFParams()
	kek := crypto.DeriveKEK([]byte("legacy-pw"), salt, params)
	wrapped, err := crypto.Seal(kek, master, nil)
	if err != nil {
		t.Fatal(err)
	}
	legacy := map[string]any{
		"version":    1,
		"created":    time.Now().UTC(),
		"kdf":        "argon2id",
		"kdf_params": params,
		"salt":       hex.EncodeToString(salt),
		"data":       hex.EncodeToString(wrapped),
		"id":         "legacyid0000000000000000000000000",
	}
	blob, _ := json.MarshalIndent(legacy, "", "  ")
	// Sanity: ensure there is truly no "type" field in the serialized form.
	if containsKey(blob, "\"type\"") {
		t.Fatal("legacy blob unexpectedly contains a type field")
	}
	if err := be2.Save(ctx, "keys/legacyid0000000000000000000000000", bytesReader(blob), int64(len(blob))); err != nil {
		t.Fatal(err)
	}

	// The legacy repo must open with the password.
	be3, _ := local.New(dir)
	rr, err := Open(ctx, be3, []byte("legacy-pw"))
	if err != nil {
		t.Fatalf("legacy repo failed to open: %v", err)
	}
	slots, _ := rr.ListKeySlots(ctx)
	if len(slots) != 1 {
		t.Fatalf("expected 1 slot, got %d", len(slots))
	}
	if slots[0].Type != KeyTypePassword {
		t.Fatalf("legacy slot should read as %q, got %q", KeyTypePassword, slots[0].Type)
	}
	rr.Close(ctx)
}

func containsKey(blob []byte, key string) bool {
	s := string(blob)
	for i := 0; i+len(key) <= len(s); i++ {
		if s[i:i+len(key)] == key {
			return true
		}
	}
	return false
}
