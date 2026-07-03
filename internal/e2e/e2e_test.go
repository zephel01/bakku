// Package e2e contains end-to-end tests exercising init -> backup -> restore
// through the repository, archiver and restorer.
package e2e

import (
	"bytes"
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/zephel01/bakku/internal/archiver"
	"github.com/zephel01/bakku/internal/backend/local"
	"github.com/zephel01/bakku/internal/repo"
	"github.com/zephel01/bakku/internal/restorer"
)

// genTree writes a small but varied directory tree into root and returns the
// map of relative path -> content for later verification.
func genTree(t *testing.T, root string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{
		"small.txt":            []byte("hello, bakku"),
		"empty.txt":            {},
		"sub/nested.txt":       bytes.Repeat([]byte("nested "), 500),
		"sub/deep/big.bin":     randBytes(5 * 1024 * 1024, 99), // multi-chunk file
		"sub/deep/dup.bin":     nil,                            // filled below (duplicate of big.bin)
		"another/readme.md":    []byte("# readme\n"),
	}
	// Make dup.bin identical to big.bin to exercise cross-file dedup.
	files["sub/deep/dup.bin"] = files["sub/deep/big.bin"]

	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Add a symlink on platforms that support it.
	if runtime.GOOS != "windows" {
		link := filepath.Join(root, "sub", "link_to_small")
		if err := os.Symlink(filepath.Join(root, "small.txt"), link); err == nil {
			files["sub/link_to_small"] = nil // marker; verified separately
		}
	}
	return files
}

func randBytes(n int, seed int64) []byte {
	r := rand.New(rand.NewSource(seed))
	b := make([]byte, n)
	r.Read(b)
	return b
}

func TestE2EBackupRestore(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	src := filepath.Join(base, "src")
	repoDir := filepath.Join(base, "repo")
	restoreDir := filepath.Join(base, "restore")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	files := genTree(t, src)
	password := []byte("e2e-password")

	// init
	be, err := local.New(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Init(ctx, be, password); err != nil {
		t.Fatal(err)
	}
	be.Close()

	// backup #1
	stats1 := runBackup(t, ctx, repoDir, password, src)
	if stats1.ChunksNew == 0 {
		t.Fatal("first backup stored no new chunks")
	}

	// backup #2 (no changes) must reuse chunks, not create new ones.
	stats2 := runBackup(t, ctx, repoDir, password, src)
	if stats2.ChunksNew != 0 {
		t.Fatalf("second backup created %d new chunks (expected full reuse)", stats2.ChunksNew)
	}
	if stats2.ChunksReused == 0 {
		t.Fatal("second backup reused no chunks")
	}

	// restore the latest snapshot
	be2, _ := local.New(repoDir)
	r2, err := repo.Open(ctx, be2, password)
	if err != nil {
		t.Fatal(err)
	}
	snaps, err := r2.ListSnapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(snaps))
	}
	rs := restorer.New(r2)
	if err := rs.Restore(ctx, snaps[0], restorer.Options{Target: restoreDir}); err != nil {
		t.Fatal(err)
	}
	r2.Close(ctx)

	// The source was backed up by absolute path, so it is restored under
	// <restoreDir>/<basename(src)>/...
	restoredRoot := filepath.Join(restoreDir, filepath.Base(src))
	verifyFiles(t, restoredRoot, src, files)
}

func runBackup(t *testing.T, ctx context.Context, repoDir string, password []byte, src string) *archiver.Stats {
	t.Helper()
	be, err := local.New(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	r, err := repo.Open(ctx, be, password)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close(ctx)
	a := archiver.New(r)
	_, stats, err := a.Backup(ctx, archiver.Options{Paths: []string{src}, Tags: []string{"e2e"}})
	if err != nil {
		t.Fatal(err)
	}
	return stats
}

func verifyFiles(t *testing.T, restoredRoot, src string, files map[string][]byte) {
	t.Helper()
	for rel, want := range files {
		rp := filepath.Join(restoredRoot, rel)
		fi, err := os.Lstat(rp)
		if err != nil {
			t.Errorf("restored file missing: %s: %v", rel, err)
			continue
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			// Verify the symlink resolves to the same target as the source.
			got, _ := os.Readlink(rp)
			orig, _ := os.Readlink(filepath.Join(src, rel))
			if got != orig {
				t.Errorf("symlink %s target = %q, want %q", rel, got, orig)
			}
			continue
		}
		got, err := os.ReadFile(rp)
		if err != nil {
			t.Errorf("read restored %s: %v", rel, err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("content mismatch for %s: got %d bytes, want %d bytes", rel, len(got), len(want))
		}
	}
}
