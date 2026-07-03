//go:build linux || darwin

package fs

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/zephel01/bakku/internal/repo"
)

// TestXattrRoundTrip verifies collectXattrs/setXattr round-trip a custom
// extended attribute. Skipped when the underlying filesystem does not
// support extended attributes (e.g. some overlay/tmpfs configurations),
// since that is an environment limitation, not a bakku bug.
func TestXattrRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "xattr.txt")
	if err := os.WriteFile(p, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	const attrName = "user.bakku_test"
	if err := setXattr(p, attrName, []byte("v1")); err != nil {
		t.Skipf("xattrs not supported on this filesystem: %v", err)
	}

	xattrs := collectXattrs(p)
	got, ok := xattrs[attrName]
	if !ok {
		t.Fatalf("collectXattrs did not return %q; got keys %v", attrName, keysOf(xattrs))
	}
	if string(got) != "v1" {
		t.Errorf("xattr value = %q, want %q", got, "v1")
	}

	// ApplyOwnerAndXattrs should restore it onto a second file.
	p2 := filepath.Join(dir, "xattr2.txt")
	if err := os.WriteFile(p2, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	n := repo.Node{Xattrs: map[string][]byte{attrName: []byte("v2")}}
	warnings := ApplyOwnerAndXattrs(p2, n, RestoreOptions{})
	if len(warnings) != 0 {
		t.Fatalf("ApplyOwnerAndXattrs warnings: %v", warnings)
	}
	got2 := collectXattrs(p2)
	if string(got2[attrName]) != "v2" {
		t.Errorf("restored xattr value = %q, want %q", got2[attrName], "v2")
	}
}

// TestQuarantineExcludedByDefault verifies the macOS quarantine xattr is
// skipped on restore unless RestoreQuarantine is set (Linux has no
// quarantine-attr concept, so applyOwnerAndXattrs never special-cases it
// there; this test only exercises the shared plumbing, which is safe on
// both OSes since the attribute name is simply absent from Xattrs on Linux).
func TestQuarantineExcludedByDefault(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "q.txt")
	if err := os.WriteFile(p, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	n := repo.Node{Xattrs: map[string][]byte{
		"com.apple.quarantine": []byte("0001;deadbeef;Safari;"),
		"user.other":           []byte("keep-me"),
	}}
	if warnings := ApplyOwnerAndXattrs(p, n, RestoreOptions{RestoreQuarantine: false}); len(warnings) != 0 {
		// setxattr on "user.other" might fail if xattrs are unsupported; treat
		// that as an environment skip rather than a test failure.
		t.Skipf("xattrs not supported on this filesystem: %v", warnings)
	}
	got := collectXattrs(p)
	if runtime.GOOS == "darwin" {
		if _, ok := got["com.apple.quarantine"]; ok {
			t.Error("com.apple.quarantine should not be restored when RestoreQuarantine=false")
		}
	}
	if _, ok := got["user.other"]; !ok {
		t.Error("user.other xattr should have been restored")
	}
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
