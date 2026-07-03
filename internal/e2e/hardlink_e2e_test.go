//go:build !windows

package e2e

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/zephel01/bakku/internal/archiver"
	"github.com/zephel01/bakku/internal/backend/local"
	"github.com/zephel01/bakku/internal/repo"
	"github.com/zephel01/bakku/internal/restorer"
)

// TestE2EHardLinkBackupRestore verifies that files sharing an inode within a
// single backup are (a) stored once (only one set of chunks reused across
// both names, i.e. the archiver's LinkTo path is taken for the 2nd+ name)
// and (b) restored as a real hard link (same inode) rather than as two
// independent copies. Skipped on Windows, which has no POSIX hard-link
// detection (see internal/fs/fs_windows.go).
func TestE2EHardLinkBackupRestore(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hard-link detection is POSIX-only")
	}
	ctx := context.Background()
	base := t.TempDir()
	src := filepath.Join(base, "src")
	repoDir := filepath.Join(base, "repo")
	restoreDir := filepath.Join(base, "restore")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	password := []byte("hardlink-e2e")

	content := bytes.Repeat([]byte("shared hard-linked content "), 10000) // multi-KB, single chunk is fine
	original := filepath.Join(src, "original.bin")
	linkedA := filepath.Join(src, "sub", "linked_a.bin")
	linkedB := filepath.Join(src, "sub", "linked_b.bin")
	unrelated := filepath.Join(src, "unrelated.txt")

	if err := os.WriteFile(original, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(original, linkedA); err != nil {
		t.Fatalf("os.Link: %v", err)
	}
	if err := os.Link(original, linkedB); err != nil {
		t.Fatalf("os.Link: %v", err)
	}
	if err := os.WriteFile(unrelated, []byte("independent file"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Sanity: confirm the source tree really does share one inode across the
	// three names before we assert anything about the backup of it.
	fiOriginal, _ := os.Lstat(original)
	fiA, _ := os.Lstat(linkedA)
	fiB, _ := os.Lstat(linkedB)
	if !os.SameFile(fiOriginal, fiA) || !os.SameFile(fiOriginal, fiB) {
		t.Fatal("test setup failed: original/linkedA/linkedB do not share an inode")
	}

	// init + backup
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
	a := archiver.New(r)
	id, stats, err := a.Backup(ctx, archiver.Options{Paths: []string{src}})
	if err != nil {
		t.Fatal(err)
	}
	// 4 files total (original, linked_a, linked_b, unrelated) all counted in
	// FilesNew, but only 2 distinct content payloads are actually chunked
	// (the shared content once, "independent file" once); the two hard-link
	// nodes reuse the first name's content blob ids via LinkTo instead of
	// re-chunking, so ChunksNew should reflect only the two unique payloads'
	// worth of chunks, not four.
	if stats.FilesNew != 4 {
		t.Fatalf("stats.FilesNew = %d, want 4", stats.FilesNew)
	}

	snap, err := r.FindSnapshot(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	// Walk the tree and confirm exactly one NodeFile among the hard-linked
	// trio has real Content and the other two carry a non-empty LinkTo.
	rsWalk := restorer.New(r)
	var contentNodes, linkToNodes int
	err = rsWalk.Walk(ctx, snap, func(path string, n repo.Node) error {
		if n.Type != repo.NodeFile {
			return nil
		}
		base := filepath.Base(path)
		if base != "original.bin" && base != "linked_a.bin" && base != "linked_b.bin" {
			return nil
		}
		if n.LinkTo != "" {
			linkToNodes++
		} else if len(n.Content) > 0 {
			contentNodes++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if contentNodes != 1 {
		t.Errorf("expected exactly 1 hard-linked node with real Content, got %d", contentNodes)
	}
	if linkToNodes != 2 {
		t.Errorf("expected exactly 2 hard-linked nodes with LinkTo set, got %d", linkToNodes)
	}

	// restore + verify the hard link is recreated (same inode) on disk.
	rs := restorer.New(r)
	if err := rs.Restore(ctx, snap, restorer.Options{Target: restoreDir}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	r.Close(ctx)

	restoredRoot := filepath.Join(restoreDir, filepath.Base(src))
	rOriginal := filepath.Join(restoredRoot, "original.bin")
	rLinkedA := filepath.Join(restoredRoot, "sub", "linked_a.bin")
	rLinkedB := filepath.Join(restoredRoot, "sub", "linked_b.bin")
	rUnrelated := filepath.Join(restoredRoot, "unrelated.txt")

	for _, p := range []string{rOriginal, rLinkedA, rLinkedB, rUnrelated} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("restored file missing: %s: %v", p, err)
		}
	}

	fiRestoredOriginal, err := os.Lstat(rOriginal)
	if err != nil {
		t.Fatal(err)
	}
	fiRestoredA, err := os.Lstat(rLinkedA)
	if err != nil {
		t.Fatal(err)
	}
	fiRestoredB, err := os.Lstat(rLinkedB)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(fiRestoredOriginal, fiRestoredA) {
		t.Error("restored original.bin and linked_a.bin do not share an inode (hard link not recreated)")
	}
	if !os.SameFile(fiRestoredOriginal, fiRestoredB) {
		t.Error("restored original.bin and linked_b.bin do not share an inode (hard link not recreated)")
	}

	// Content must still be correct.
	got, err := os.ReadFile(rLinkedA)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Error("restored linked_a.bin content mismatch")
	}

	// The unrelated file must NOT share an inode with the hard-linked trio.
	if os.SameFile(fiRestoredOriginal, mustLstat(t, rUnrelated)) {
		t.Error("unrelated.txt unexpectedly shares an inode with the hard-linked group")
	}
}

func mustLstat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi
}
