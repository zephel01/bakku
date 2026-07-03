package repo

import (
	"context"
	"errors"
	"testing"

	"github.com/zephel01/bakku/internal/backend/local"
	"github.com/zephel01/bakku/internal/yubikey"
)

// newTestRepo creates a fresh password-protected repository in a temp dir
// backend, returning the repo and the backend's directory (so a second Open
// can be issued against the same storage).
func newTestRepo(t *testing.T) (*Repository, string) {
	t.Helper()
	dir := t.TempDir()
	be, err := local.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Init(context.Background(), be, []byte("correct horse battery staple"))
	if err != nil {
		t.Fatal(err)
	}
	return r, dir
}

func reopenBackend(t *testing.T, dir string) *local.Local {
	t.Helper()
	be, err := local.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	return be
}

func TestAddYubiKeySlotAndUnlockRoundTrip(t *testing.T) {
	ctx := context.Background()
	r, dir := newTestRepo(t)

	mock := &yubikey.MockResponder{Secret: []byte("device-secret-1")}
	slotID, err := r.AddYubiKeySlot(ctx, mock, 2)
	if err != nil {
		t.Fatal(err)
	}
	if slotID == "" {
		t.Fatal("expected non-empty slot id")
	}
	// AddYubiKeySlot exchanges the challenge twice to confirm stability.
	if mock.Calls != 2 {
		t.Fatalf("Calls = %d, want 2 (stability check)", mock.Calls)
	}
	if err := r.Close(ctx); err != nil {
		t.Fatal(err)
	}

	// Re-open using only the YubiKey (a fresh mock instance simulating the
	// same physical device: same secret).
	be := reopenBackend(t, dir)
	freshMock := &yubikey.MockResponder{Secret: []byte("device-secret-1")}
	r2, err := OpenWithYubiKey(ctx, be, freshMock)
	if err != nil {
		t.Fatalf("OpenWithYubiKey failed: %v", err)
	}
	defer r2.Close(ctx)

	if r2.OpenedKeyID() != slotID {
		t.Fatalf("opened key id = %s, want %s", r2.OpenedKeyID(), slotID)
	}

	// The password slot must still work too (multi-slot: lose one, use another).
	be2 := reopenBackend(t, dir)
	r3, err := Open(ctx, be2, []byte("correct horse battery staple"))
	if err != nil {
		t.Fatalf("password Open failed after adding yubikey slot: %v", err)
	}
	defer r3.Close(ctx)
}

func TestYubiKeyUnlockWrongDeviceFails(t *testing.T) {
	ctx := context.Background()
	r, dir := newTestRepo(t)

	mock := &yubikey.MockResponder{Secret: []byte("device-secret-1")}
	if _, err := r.AddYubiKeySlot(ctx, mock, 2); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(ctx); err != nil {
		t.Fatal(err)
	}

	be := reopenBackend(t, dir)
	wrongDevice := &yubikey.MockResponder{Secret: []byte("some-other-secret")}
	_, err := OpenWithYubiKey(ctx, be, wrongDevice)
	if !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("expected ErrWrongPassword for mismatched response, got %v", err)
	}
}

func TestYubiKeyChallengeDistinctPerSlot(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)

	mock1 := &yubikey.MockResponder{Secret: []byte("secret-a")}
	id1, err := r.AddYubiKeySlot(ctx, mock1, 2)
	if err != nil {
		t.Fatal(err)
	}
	mock2 := &yubikey.MockResponder{Secret: []byte("secret-b")}
	id2, err := r.AddYubiKeySlot(ctx, mock2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 {
		t.Fatal("two AddYubiKeySlot calls produced the same slot id")
	}

	slots, err := listKeySlots(ctx, r.be)
	if err != nil {
		t.Fatal(err)
	}
	var challenges [][]byte
	for _, s := range slots {
		if s.kf.effectiveType() != KeyTypeYubiKey {
			continue
		}
		_, ch, err := slotParams(s.kf)
		if err != nil {
			t.Fatal(err)
		}
		challenges = append(challenges, ch)
	}
	if len(challenges) != 2 {
		t.Fatalf("expected 2 yubikey slots, got %d", len(challenges))
	}
	if bytesEqual(challenges[0], challenges[1]) {
		t.Fatal("two yubikey slots got the identical challenge (should be independently random)")
	}
}

// unstableResponder returns a different response every call, simulating a
// misconfigured YubiKey slot (e.g. HOTP-only, not HMAC-SHA1 chal-resp).
type unstableResponder struct{ n int }

func (u *unstableResponder) Respond(_ context.Context, _ int, _ []byte) ([yubikey.ResponseSize]byte, error) {
	u.n++
	var out [yubikey.ResponseSize]byte
	out[0] = byte(u.n) // differs each call
	return out, nil
}

func TestAddYubiKeySlotRejectsUnstableResponse(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)

	_, err := r.AddYubiKeySlot(ctx, &unstableResponder{}, 2)
	if err == nil {
		t.Fatal("expected an error when the two challenge-response exchanges disagree")
	}
}

func TestHasYubiKeySlot(t *testing.T) {
	ctx := context.Background()
	r, dir := newTestRepo(t)

	has, err := HasYubiKeySlot(ctx, r.be)
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Fatal("fresh password-only repo should report no yubikey slot")
	}

	mock := &yubikey.MockResponder{Secret: []byte("s")}
	if _, err := r.AddYubiKeySlot(ctx, mock, 2); err != nil {
		t.Fatal(err)
	}
	has, err = HasYubiKeySlot(ctx, r.be)
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Fatal("expected HasYubiKeySlot == true after AddYubiKeySlot")
	}
	_ = dir
}

func TestKeyListShowsYubiKeyType(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)

	mock := &yubikey.MockResponder{Secret: []byte("s")}
	id, err := r.AddYubiKeySlot(ctx, mock, 2)
	if err != nil {
		t.Fatal(err)
	}
	slots, err := r.ListKeySlots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range slots {
		if s.ID == id {
			found = true
			if s.Type != KeyTypeYubiKey {
				t.Fatalf("slot type = %q, want %q", s.Type, KeyTypeYubiKey)
			}
		}
	}
	if !found {
		t.Fatal("added yubikey slot not found in ListKeySlots")
	}
}
