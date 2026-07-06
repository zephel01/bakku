package restorer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zephel01/bakku/internal/repo"
)

// TestRestoreNodesRejectsTraversal verifies that a crafted tree carrying a
// path-traversal name ("..", "../x", absolute, or an embedded separator) is
// refused before any filesystem write, so a malicious/corrupted snapshot
// cannot escape the restore target (zip-slip). The names are caught by
// fs.SafeJoin prior to any repository access, so a nil repo is sufficient.
func TestRestoreNodesRejectsTraversal(t *testing.T) {
	target := t.TempDir()
	guard := filepath.Join(filepath.Dir(target), "escaped.txt")
	t.Cleanup(func() { _ = os.Remove(guard) })

	malicious := []string{"..", "../escaped.txt", "../../escaped.txt", "a/b", "/etc/passwd"}
	for _, name := range malicious {
		rs := &Restorer{target: filepath.Clean(target)}
		nodes := []repo.Node{{Name: name, Type: repo.NodeSymlink, LinkTarget: "/tmp/x"}}
		err := rs.restoreNodes(context.Background(), nodes, target, "", Options{})
		if err == nil {
			t.Errorf("restoreNodes(name=%q) = nil, want traversal rejection", name)
		} else if !strings.Contains(err.Error(), "restorer:") {
			t.Errorf("restoreNodes(name=%q) unexpected error: %v", name, err)
		}
	}

	if _, err := os.Stat(guard); err == nil {
		t.Fatalf("traversal wrote outside target: %s exists", guard)
	}
}

// TestRestoreNodesAcceptsSafeName confirms a legitimate single-element symlink
// name is not rejected by the containment check (it proceeds to create the
// symlink under the target).
func TestRestoreNodesAcceptsSafeName(t *testing.T) {
	target := t.TempDir()
	rs := &Restorer{target: filepath.Clean(target)}
	nodes := []repo.Node{{Name: "link", Type: repo.NodeSymlink, LinkTarget: "/tmp/x"}}
	if err := rs.restoreNodes(context.Background(), nodes, target, "", Options{}); err != nil {
		t.Fatalf("restoreNodes(safe name) = %v, want nil", err)
	}
	if _, err := os.Lstat(filepath.Join(target, "link")); err != nil {
		t.Fatalf("expected symlink created under target: %v", err)
	}
}
