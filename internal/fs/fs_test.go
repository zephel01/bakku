package fs

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestCollectOwnership verifies Collect reports the current process's
// uid/gid for a freshly created file on POSIX platforms.
func TestCollectOwnership(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ownership is not POSIX-shaped on windows")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(p, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	owner, hasOwner, _, _, _ := Collect(p, fi)
	if !hasOwner {
		t.Fatal("expected hasOwner=true on a POSIX platform")
	}
	if int(owner.UID) != os.Getuid() {
		t.Errorf("owner.UID = %d, want %d", owner.UID, os.Getuid())
	}
	if int(owner.GID) != os.Getgid() {
		t.Errorf("owner.GID = %d, want %d", owner.GID, os.Getgid())
	}
}

// TestCollectHardLink verifies Collect detects a shared inode across two
// hard-linked names and that unlinked (Nlink==1) files report hasLink=false.
func TestCollectHardLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hard-link detection via inode+dev is POSIX-only")
	}
	dir := t.TempDir()
	original := filepath.Join(dir, "original.txt")
	linked := filepath.Join(dir, "linked.txt")
	solo := filepath.Join(dir, "solo.txt")

	if err := os.WriteFile(original, []byte("shared content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(original, linked); err != nil {
		t.Fatalf("os.Link: %v", err)
	}
	if err := os.WriteFile(solo, []byte("solo content"), 0o644); err != nil {
		t.Fatal(err)
	}

	fiOriginal, err := os.Lstat(original)
	if err != nil {
		t.Fatal(err)
	}
	fiLinked, err := os.Lstat(linked)
	if err != nil {
		t.Fatal(err)
	}
	fiSolo, err := os.Lstat(solo)
	if err != nil {
		t.Fatal(err)
	}

	_, _, _, keyOriginal, hasLinkOriginal := Collect(original, fiOriginal)
	_, _, _, keyLinked, hasLinkLinked := Collect(linked, fiLinked)
	_, _, _, _, hasLinkSolo := Collect(solo, fiSolo)

	if !hasLinkOriginal || !hasLinkLinked {
		t.Fatalf("expected both hard-linked names to report hasLink=true, got original=%v linked=%v", hasLinkOriginal, hasLinkLinked)
	}
	if keyOriginal != keyLinked {
		t.Errorf("LinkKey mismatch: original=%+v linked=%+v, want equal", keyOriginal, keyLinked)
	}
	if hasLinkSolo {
		t.Error("solo (Nlink==1) file should report hasLink=false")
	}
}

// TestIsRoot sanity-checks IsRoot against os.Geteuid()/os.Getuid() semantics
// without requiring the test to actually run as root.
func TestIsRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("IsRoot on windows checks process elevation, not euid")
	}
	want := os.Geteuid() == 0
	if got := IsRoot(); got != want {
		t.Errorf("IsRoot() = %v, want %v (euid=%d)", got, want, os.Geteuid())
	}
}
