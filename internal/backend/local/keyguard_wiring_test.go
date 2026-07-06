package local

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// TestKeyguardWiring confirms the backend rejects traversal keys at every
// entry point (defense in depth), so a "../" key can never resolve outside the
// repository root even if one reached the backend.
func TestKeyguardWiring(t *testing.T) {
	ctx := context.Background()
	be, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bad := "../escape"

	if err := be.Save(ctx, bad, bytes.NewReader([]byte("x")), 1); err == nil {
		t.Error("Save accepted traversal key")
	}
	if _, err := be.Load(ctx, bad, 0, -1); err == nil {
		t.Error("Load accepted traversal key")
	}
	if _, err := be.Stat(ctx, bad); err == nil {
		t.Error("Stat accepted traversal key")
	}
	if err := be.Delete(ctx, bad); err == nil {
		t.Error("Delete accepted traversal key")
	}
	if err := be.List(ctx, "../", func(string, int64) error { return nil }); err == nil {
		t.Error("List accepted traversal prefix")
	}

	// A normal key still works.
	if err := be.Save(ctx, "data/ab/cd", bytes.NewReader([]byte("ok")), 2); err != nil {
		t.Fatalf("Save rejected a legitimate key: %v", err)
	}
	rc, err := be.Load(ctx, "data/ab/cd", 0, -1)
	if err != nil {
		t.Fatalf("Load rejected a legitimate key: %v", err)
	}
	defer rc.Close()
	got := make([]byte, 2)
	_, _ = rc.Read(got)
	if !strings.HasPrefix(string(got), "ok") {
		t.Fatalf("round-trip content mismatch: %q", got)
	}
}
