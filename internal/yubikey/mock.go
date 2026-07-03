package yubikey

import (
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // HMAC-SHA1 is the YubiKey OTP challenge-response algorithm itself, not used for signature/collision security here.
)

// MockResponder simulates a YubiKey's HMAC-SHA1 challenge-response OTP slot
// entirely in memory, for tests. It computes a real HMAC-SHA1 over a fixed
// per-instance secret, so it behaves like hardware: the same challenge always
// yields the same response, and different secrets (simulating different
// physical keys) yield different responses.
type MockResponder struct {
	// Secret is the simulated per-slot HMAC key programmed on the "device".
	Secret []byte
	// Slot, if non-zero, restricts this mock to only answering for that OTP
	// slot; a mismatched slot returns ErrSlotMismatch. Zero means "answer any
	// slot" (useful when a test does not care about slot routing).
	Slot int
	// Calls counts how many times Respond has been invoked (for assertions
	// like "the CLI re-challenged twice to confirm stability").
	Calls int
}

// ErrSlotMismatch is returned by MockResponder when asked to respond on a
// slot other than the one it was configured for.
var ErrSlotMismatch = &slotMismatchError{}

type slotMismatchError struct{}

func (*slotMismatchError) Error() string { return "yubikey: mock configured for a different slot" }

// Respond implements Responder using HMAC-SHA1(Secret, challenge).
func (m *MockResponder) Respond(_ context.Context, slot int, challenge []byte) ([ResponseSize]byte, error) {
	m.Calls++
	var out [ResponseSize]byte
	if m.Slot != 0 && slot != m.Slot {
		return out, ErrSlotMismatch
	}
	mac := hmac.New(sha1.New, m.Secret)
	mac.Write(challenge)
	sum := mac.Sum(nil)
	copy(out[:], sum)
	return out, nil
}
