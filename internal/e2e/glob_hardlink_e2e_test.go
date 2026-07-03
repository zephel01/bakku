package e2e

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/zephel01/bakku/internal/archiver"
	"github.com/zephel01/bakku/internal/backend/local"
	"github.com/zephel01/bakku/internal/repo"
	"github.com/zephel01/bakku/internal/restorer"
)

// openRepoWith initializes a repository under base/repo, returns an opened
// *repo.Repository plus a cleanup, using the given password.
func openRepoWith(t *testing.T, base string, password []byte) (*repo.Repository, func()) {
	t.Helper()
	ctx := context.Background()
	repoDir := filepath.Join(base, "repo")
	be, err := local.New(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Init(ctx, be, password); err != nil {
		t.Fatal(err)
	}
	be.Close()

	be2, err := local.New(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	r, err := repo.Open(ctx, be2, password)
	if err != nil {
		t.Fatal(err)
	}
	return r, func() { r.Close(ctx) }
}

// TestE2EExcludeDoublestar verifies that `--exclude '**/node_modules/**'` skips
// everything under any node_modules directory at any depth, which the previous
// filepath.Match-based matcher could not do.
func TestE2EExcludeDoublestar(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	src := filepath.Join(base, "src")
	restoreDir := filepath.Join(base, "restore")

	files := map[string][]byte{
		"app/main.js":                       []byte("keep me"),
		"app/node_modules/pkg/index.js":     []byte("drop me deep"),
		"node_modules/root-pkg/index.js":    []byte("drop me shallow"),
		"lib/node_modules/x/y/z/deep.js":    []byte("drop me deeper"),
		"docs/readme.md":                    []byte("keep docs"),
	}
	for rel, content := range files {
		p := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	r, cleanup := openRepoWith(t, base, []byte("exclude-e2e"))
	defer cleanup()

	a := archiver.New(r)
	id, _, err := a.Backup(ctx, archiver.Options{
		Paths:    []string{src},
		Excludes: []string{"**/node_modules/**"},
	})
	if err != nil {
		t.Fatal(err)
	}

	snap, err := r.FindSnapshot(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	// Confirm no node_modules path is present in the snapshot tree.
	rs := restorer.New(r)
	err = rs.Walk(ctx, snap, func(path string, n repo.Node) error {
		if n.Type == repo.NodeFile && containsSegment(path, "node_modules") {
			t.Errorf("node_modules file not excluded: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Restore and assert the kept files exist, the excluded ones do not.
	if err := rs.Restore(ctx, snap, restorer.Options{Target: restoreDir}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	root := filepath.Join(restoreDir, filepath.Base(src))
	for _, keep := range []string{"app/main.js", "docs/readme.md"} {
		if _, err := os.Stat(filepath.Join(root, keep)); err != nil {
			t.Errorf("expected kept file missing: %s: %v", keep, err)
		}
	}
	for _, drop := range []string{
		"app/node_modules/pkg/index.js",
		"node_modules/root-pkg/index.js",
		"lib/node_modules/x/y/z/deep.js",
	} {
		if _, err := os.Stat(filepath.Join(root, drop)); err == nil {
			t.Errorf("expected excluded file to be absent: %s", drop)
		}
	}
}

// TestE2EIncludeDoublestar verifies that `--include '**/doc1.txt'` restores only
// the matching file at any depth.
func TestE2EIncludeDoublestar(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	src := filepath.Join(base, "src")
	restoreDir := filepath.Join(base, "restore")

	files := map[string][]byte{
		"docs/doc1.txt":       []byte("doc one"),
		"docs/doc2.txt":       []byte("doc two"),
		"a/b/c/doc1.txt":      []byte("nested doc one"),
		"other/report.xlsx":   []byte("xlsx"),
	}
	for rel, content := range files {
		p := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	r, cleanup := openRepoWith(t, base, []byte("include-e2e"))
	defer cleanup()

	a := archiver.New(r)
	id, _, err := a.Backup(ctx, archiver.Options{Paths: []string{src}})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := r.FindSnapshot(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	rs := restorer.New(r)
	if err := rs.Restore(ctx, snap, restorer.Options{
		Target:   restoreDir,
		Includes: []string{"**/doc1.txt"},
	}); err != nil {
		t.Fatalf("restore: %v", err)
	}

	root := filepath.Join(restoreDir, filepath.Base(src))
	// Both doc1.txt at any depth should be restored.
	for _, want := range []string{"docs/doc1.txt", "a/b/c/doc1.txt"} {
		got, err := os.ReadFile(filepath.Join(root, want))
		if err != nil {
			t.Errorf("expected included file %s missing: %v", want, err)
			continue
		}
		if !bytes.Equal(got, files[want]) {
			t.Errorf("content mismatch for %s", want)
		}
	}
	// Non-matching files must not be restored.
	for _, absent := range []string{"docs/doc2.txt", "other/report.xlsx"} {
		if _, err := os.Stat(filepath.Join(root, absent)); err == nil {
			t.Errorf("expected non-included file absent: %s", absent)
		}
	}
}

// TestE2EHardLinkPartialRestore verifies that restoring only one side of a
// hard-link pair (via --include) succeeds with content and exit-equivalent
// success (no error returned), materializing the included node as an
// independent regular file even though its LinkTo target was excluded.
func TestE2EHardLinkPartialRestore(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hard-link detection is POSIX-only")
	}
	ctx := context.Background()
	base := t.TempDir()
	src := filepath.Join(base, "src")
	restoreDir := filepath.Join(base, "restore")
	if err := os.MkdirAll(filepath.Join(src, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}

	content := bytes.Repeat([]byte("hard-linked doc content "), 2000)
	doc1 := filepath.Join(src, "docs", "doc1.txt")
	hardlink := filepath.Join(src, "hardlink-doc")
	if err := os.WriteFile(doc1, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(doc1, hardlink); err != nil {
		t.Fatalf("os.Link: %v", err)
	}

	r, cleanup := openRepoWith(t, base, []byte("hl-partial-e2e"))
	defer cleanup()

	a := archiver.New(r)
	id, _, err := a.Backup(ctx, archiver.Options{Paths: []string{src}})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := r.FindSnapshot(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	// Include only doc1.txt. Depending on archive order, doc1.txt may be the
	// content-bearing node OR the LinkTo node whose target (hardlink-doc) is
	// excluded. Either way the restore must succeed with content.
	rs := restorer.New(r)
	if err := rs.Restore(ctx, snap, restorer.Options{
		Target:   restoreDir,
		Includes: []string{"**/doc1.txt"},
	}); err != nil {
		t.Fatalf("partial hard-link restore returned error (should succeed): %v", err)
	}

	root := filepath.Join(restoreDir, filepath.Base(src))
	rDoc1 := filepath.Join(root, "docs", "doc1.txt")
	got, err := os.ReadFile(rDoc1)
	if err != nil {
		t.Fatalf("doc1.txt not restored: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("doc1.txt content mismatch: got %d bytes, want %d", len(got), len(content))
	}
	// The excluded hardlink-doc must NOT be restored.
	if _, err := os.Stat(filepath.Join(root, "hardlink-doc")); err == nil {
		t.Errorf("excluded hardlink-doc should not be restored")
	}
	// No warnings expected: the content was recoverable from the recorded node.
	if w := rs.Warnings(); len(w) != 0 {
		t.Logf("warnings (non-fatal): %v", w)
	}
}

// containsSegment reports whether seg is a path segment of p (OS-separated).
func containsSegment(p, seg string) bool {
	for _, part := range strings.Split(filepath.ToSlash(p), "/") {
		if part == seg {
			return true
		}
	}
	return false
}
