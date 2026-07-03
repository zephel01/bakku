package keychain

import (
	"errors"
	"testing"
)

func TestNormalizeKey(t *testing.T) {
	cases := map[string]string{
		"file:///backups/x":   "file:///backups/x",
		"file:///backups/x/ ": "file:///backups/x",
		" file:///a/ ":        "file:///a",
	}
	for in, want := range cases {
		if got := normalizeKey(in); got != want {
			t.Fatalf("normalizeKey(%q)=%q want %q", in, got, want)
		}
	}
}

// TestGetFallsThroughWhenUnavailable verifies that on a headless host with no
// secret service (the sandbox), Get returns a sentinel error rather than
// panicking, so callers can fall through to the next password source.
func TestGetFallsThroughWhenUnavailable(t *testing.T) {
	_, err := Get("file:///definitely/not/stored/anywhere")
	if err == nil {
		t.Skip("secret store is available in this environment; nothing to assert")
	}
	if !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrNotFound or ErrUnavailable, got %v", err)
	}
}
