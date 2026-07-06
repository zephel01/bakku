package fs

import (
	"path/filepath"
	"testing"
)

func TestValidRestoreName(t *testing.T) {
	good := []string{"file.txt", "a", "名前.md", "with space", ".hidden", "..."}
	for _, n := range good {
		if !ValidRestoreName(n) {
			t.Errorf("ValidRestoreName(%q) = false, want true", n)
		}
	}
	bad := []string{"", ".", "..", "a/b", "../etc", "/abs", "a\x00b"}
	for _, n := range bad {
		if ValidRestoreName(n) {
			t.Errorf("ValidRestoreName(%q) = true, want false", n)
		}
	}
}

func TestWithinRoot(t *testing.T) {
	root := filepath.Clean("/restore/root")
	in := []string{
		"/restore/root",
		"/restore/root/a",
		"/restore/root/a/b/c",
		"/restore/root/./a",
	}
	for _, p := range in {
		if !WithinRoot(root, p) {
			t.Errorf("WithinRoot(%q, %q) = false, want true", root, p)
		}
	}
	out := []string{
		"/restore/rootx",       // sibling prefix, not a child
		"/restore",             // parent
		"/restore/root/../etc", // escapes via ..
		"/etc/passwd",
	}
	for _, p := range out {
		if WithinRoot(root, p) {
			t.Errorf("WithinRoot(%q, %q) = true, want false", root, p)
		}
	}
}

func TestSafeJoin(t *testing.T) {
	root := filepath.Clean("/restore/root")
	dir := filepath.Join(root, "sub")

	if got, err := SafeJoin(root, dir, "file.txt"); err != nil || got != filepath.Join(dir, "file.txt") {
		t.Fatalf("SafeJoin good case: got %q err %v", got, err)
	}

	for _, name := range []string{"..", "../../etc/passwd", "a/b", "/abs", ""} {
		if _, err := SafeJoin(root, dir, name); err == nil {
			t.Errorf("SafeJoin(%q) = nil error, want rejection", name)
		}
	}
}
