package e2e

import (
	"bytes"
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/zephel01/bakku/internal/archiver"
	"github.com/zephel01/bakku/internal/backend/local"
	"github.com/zephel01/bakku/internal/crypto"
	"github.com/zephel01/bakku/internal/pruner"
	"github.com/zephel01/bakku/internal/repo"
	"github.com/zephel01/bakku/internal/restorer"
)

// TestE2EForgetPruneCheckRestore exercises the full maintenance pipeline:
//
//	backup x3 -> forget --keep-last 1 -> prune -> check --read-data ->
//	restore the surviving snapshot and verify contents.
func TestE2EForgetPruneCheckRestore(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	src := filepath.Join(base, "src")
	repoDir := filepath.Join(base, "repo")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	password := []byte("maint-e2e")

	// init
	be, err := local.New(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Init(ctx, be, password); err != nil {
		t.Fatal(err)
	}
	be.Close()

	// Three backups, mutating the tree each time so each snapshot introduces new
	// unique blobs (which become garbage once earlier snapshots are forgotten).
	backup := func(round int, files map[string][]byte) {
		for rel, content := range files {
			p := filepath.Join(src, rel)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, content, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		be, _ := local.New(repoDir)
		r, err := repo.Open(ctx, be, password)
		if err != nil {
			t.Fatal(err)
		}
		a := archiver.New(r)
		if _, _, err := a.Backup(ctx, archiver.Options{Paths: []string{src}}); err != nil {
			t.Fatal(err)
		}
		if err := r.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}

	backup(1, map[string][]byte{
		"keep.txt":   []byte("stable file that survives all rounds"),
		"round1.bin": randBytes(2*1024*1024, 1),
	})
	backup(2, map[string][]byte{
		"round1.bin": randBytes(2*1024*1024, 2), // overwrite -> new blobs
		"round2.bin": randBytes(2*1024*1024, 3),
	})
	// Final state for round 3 (this is what must be restorable at the end).
	finalFiles := map[string][]byte{
		"keep.txt":   []byte("stable file that survives all rounds"),
		"round1.bin": randBytes(2*1024*1024, 4),
		"round2.bin": randBytes(2*1024*1024, 3),
		"round3.bin": randBytes(2*1024*1024, 5),
	}
	backup(3, finalFiles)

	// --- forget --keep-last 1 ---
	be2, _ := local.New(repoDir)
	r, err := repo.Open(ctx, be2, password)
	if err != nil {
		t.Fatal(err)
	}
	snaps, err := r.ListSnapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 3 {
		t.Fatalf("expected 3 snapshots, got %d", len(snaps))
	}
	decs := pruner.ApplyGrouped(snaps, pruner.Policy{KeepLast: 1})
	var survivor *repo.Snapshot
	forgot := 0
	for _, d := range decs {
		if d.Keep {
			survivor = d.Snapshot
			continue
		}
		if err := r.DeleteSnapshot(ctx, d.Snapshot.ID); err != nil {
			t.Fatal(err)
		}
		forgot++
	}
	if forgot != 2 || survivor == nil {
		t.Fatalf("forget kept the wrong count: forgot=%d survivor=%v", forgot, survivor)
	}
	// The survivor must be the newest (round 3) snapshot.
	if survivor.ID != snaps[0].ID {
		t.Fatalf("survivor %s is not the newest snapshot %s", survivor.ID, snaps[0].ID)
	}

	// --- prune ---
	remaining, err := r.ListSnapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	packsBefore, _ := r.ListPacks(ctx)
	plan, err := pruner.BuildPlan(ctx, r, remaining)
	if err != nil {
		t.Fatal(err)
	}
	st, err := pruner.Execute(ctx, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	if st.ReclaimedBytes == 0 {
		t.Fatal("prune reclaimed no bytes despite two forgotten snapshots")
	}
	packsAfter, _ := r.ListPacks(ctx)
	t.Logf("packs %d -> %d, reclaimed %d bytes, %d new packs",
		len(packsBefore), len(packsAfter), st.ReclaimedBytes, st.NewPacks)

	// --- check --read-data (structural + cryptographic) ---
	// Every indexed blob must live in an existing pack.
	packSet := map[string]struct{}{}
	for _, p := range packsAfter {
		packSet[p.ID] = struct{}{}
	}
	for id, loc := range r.IndexEntries() {
		if _, ok := packSet[loc.PackID]; !ok {
			t.Fatalf("post-prune: index blob %s points at missing pack %s", id, loc.PackID)
		}
	}
	// Every blob reachable from the survivor must be in the index.
	used, err := pruner.Reachable(ctx, r, remaining)
	if err != nil {
		t.Fatal(err)
	}
	index := r.IndexEntries()
	for id := range used {
		if _, ok := index[id]; !ok {
			t.Fatalf("post-prune: reachable blob %s missing from index", id)
		}
	}
	// read-data: verify every pack cryptographically.
	for _, p := range packsAfter {
		if errs := r.VerifyPack(ctx, p.ID); len(errs) > 0 {
			t.Fatalf("check --read-data failed for pack %s: %v", p.ID, errs)
		}
	}
	// Independent hash re-verification of every reachable data blob.
	for id := range used {
		blob, err := r.LoadBlob(ctx, id)
		if err != nil {
			t.Fatalf("post-prune load blob %s: %v", id, err)
		}
		h := crypto.HashChunk(blob)
		if hex.EncodeToString(h[:]) != id {
			t.Fatalf("post-prune blob %s hash mismatch", id)
		}
	}

	// --- restore the surviving snapshot and verify contents ---
	restoreDir := filepath.Join(base, "restore")
	rs := restorer.New(r)
	if err := rs.Restore(ctx, survivor, restorer.Options{Target: restoreDir}); err != nil {
		t.Fatalf("restore after prune failed: %v", err)
	}
	r.Close(ctx)

	restoredRoot := filepath.Join(restoreDir, filepath.Base(src))
	for rel, want := range finalFiles {
		got, err := os.ReadFile(filepath.Join(restoredRoot, rel))
		if err != nil {
			t.Errorf("restored file missing %s: %v", rel, err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("restored %s content mismatch: got %d bytes, want %d", rel, len(got), len(want))
		}
	}
}
