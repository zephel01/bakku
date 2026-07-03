package pruner

import (
	"context"
	"testing"
	"time"

	"github.com/zephel01/bakku/internal/backend/local"
	"github.com/zephel01/bakku/internal/repo"
)

// openTestRepo initializes a fresh repository in a temp dir.
func openTestRepo(t *testing.T) (*repo.Repository, string) {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	be, err := local.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Init(ctx, be, []byte("pw")); err != nil {
		t.Fatal(err)
	}
	be.Close()
	be2, _ := local.New(dir)
	r, err := repo.Open(ctx, be2, []byte("pw"))
	if err != nil {
		t.Fatal(err)
	}
	return r, dir
}

// makeSnapshot stores a single-file tree with the given content and returns the
// snapshot.
func makeSnapshot(t *testing.T, r *repo.Repository, name string, content []byte) *repo.Snapshot {
	t.Helper()
	ctx := context.Background()
	blobID, _, err := r.SaveBlob(ctx, repo.BlobData, content)
	if err != nil {
		t.Fatal(err)
	}
	treeID, err := r.SaveTree(ctx, []repo.Node{{
		Name:    name,
		Type:    repo.NodeFile,
		Size:    int64(len(content)),
		Content: []string{blobID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	snap := &repo.Snapshot{
		Time:     time.Now(),
		Hostname: "h",
		Paths:    []string{"/x"},
		Tree:     treeID,
	}
	if _, err := r.SaveSnapshot(ctx, snap); err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestReachable(t *testing.T) {
	ctx := context.Background()
	r, _ := openTestRepo(t)
	defer r.Close(ctx)

	s1 := makeSnapshot(t, r, "a.txt", []byte("alpha content one"))
	s2 := makeSnapshot(t, r, "b.txt", []byte("beta content two"))

	used, err := Reachable(ctx, r, []*repo.Snapshot{s1, s2})
	if err != nil {
		t.Fatal(err)
	}
	// Each snapshot contributes 1 tree blob + 1 data blob = 4 distinct blobs.
	if len(used) != 4 {
		t.Fatalf("reachable set = %d blobs, want 4", len(used))
	}
	// Tree ids must be marked used.
	if _, ok := used[s1.Tree]; !ok {
		t.Fatal("s1 tree not marked reachable")
	}
	if _, ok := used[s2.Tree]; !ok {
		t.Fatal("s2 tree not marked reachable")
	}
}

// makePartialPackSnapshots creates two snapshots whose blobs all land in ONE
// pack (a single Flush), so that deleting one snapshot leaves a partially-used
// pack that must be repacked.
func makePartialPackSnapshots(t *testing.T, r *repo.Repository) (dead, live *repo.Snapshot) {
	t.Helper()
	ctx := context.Background()

	deadBlob, _, err := r.SaveBlob(ctx, repo.BlobData, []byte("dead file content one two"))
	if err != nil {
		t.Fatal(err)
	}
	liveBlob, _, err := r.SaveBlob(ctx, repo.BlobData, []byte("live file content three four"))
	if err != nil {
		t.Fatal(err)
	}
	deadTree, err := r.SaveTree(ctx, []repo.Node{{Name: "dead.txt", Type: repo.NodeFile, Content: []string{deadBlob}}})
	if err != nil {
		t.Fatal(err)
	}
	liveTree, err := r.SaveTree(ctx, []repo.Node{{Name: "live.txt", Type: repo.NodeFile, Content: []string{liveBlob}}})
	if err != nil {
		t.Fatal(err)
	}
	// Single flush: all four blobs (2 data + 2 tree) go into one pack.
	if err := r.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	dead = &repo.Snapshot{Time: time.Now(), Hostname: "h", Paths: []string{"/x"}, Tree: deadTree}
	live = &repo.Snapshot{Time: time.Now(), Hostname: "h", Paths: []string{"/x"}, Tree: liveTree}
	if _, err := r.SaveSnapshot(ctx, dead); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SaveSnapshot(ctx, live); err != nil {
		t.Fatal(err)
	}
	return dead, live
}

func TestBuildPlanAndExecute(t *testing.T) {
	ctx := context.Background()
	r, dir := openTestRepo(t)

	s1, s2 := makePartialPackSnapshots(t, r)

	// Forget s1 by deleting its snapshot record; its blobs become unreferenced.
	if err := r.DeleteSnapshot(ctx, s1.ID); err != nil {
		t.Fatal(err)
	}

	remaining, err := r.ListSnapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || remaining[0].ID != s2.ID {
		t.Fatalf("expected only s2 to remain, got %+v", remaining)
	}

	plan, err := BuildPlan(ctx, r, remaining)
	if err != nil {
		t.Fatal(err)
	}
	// s2 keeps 2 blobs (tree + data). s1's 2 blobs are dead. Because all four
	// blobs were flushed into the same pack, the pack is PARTIAL and must be
	// repacked.
	if len(plan.UsedBlobs) != 2 {
		t.Fatalf("used blobs = %d, want 2", len(plan.UsedBlobs))
	}
	if len(plan.RepackBlobs) != 2 {
		t.Fatalf("repack blobs = %d, want 2 (partial pack)", len(plan.RepackBlobs))
	}

	st, err := Execute(ctx, r, plan)
	if err != nil {
		t.Fatal(err)
	}
	if st.NewPacks == 0 {
		t.Fatal("expected at least one new pack from repack")
	}
	if st.PacksDeleted == 0 {
		t.Fatal("expected the old partial pack to be deleted")
	}

	// After prune, s2 must still be fully restorable: its two blobs must be
	// loadable via the rebuilt index.
	nodes, err := r.LoadTree(ctx, s2.Tree)
	if err != nil {
		t.Fatalf("post-prune load tree failed: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("post-prune tree has %d nodes, want 1", len(nodes))
	}
	blob, err := r.LoadBlob(ctx, nodes[0].Content[0])
	if err != nil {
		t.Fatalf("post-prune load blob failed: %v", err)
	}
	if string(blob) != "live file content three four" {
		t.Fatalf("post-prune blob content wrong: %q", blob)
	}
	r.Close(ctx)

	// Reopen the repository from scratch to confirm the on-disk index is
	// consistent and self-sufficient after the pack/index swap.
	be, _ := local.New(dir)
	r2, err := repo.Open(ctx, be, []byte("pw"))
	if err != nil {
		t.Fatalf("reopen failed: %v", err)
	}
	defer r2.Close(ctx)
	nodes2, err := r2.LoadTree(ctx, s2.Tree)
	if err != nil {
		t.Fatalf("reopen load tree failed: %v", err)
	}
	if _, err := r2.LoadBlob(ctx, nodes2[0].Content[0]); err != nil {
		t.Fatalf("reopen load blob failed: %v", err)
	}
}

// TestPruneFullyUnusedPack verifies a pack with no live blobs is deleted
// outright (not repacked).
func TestPruneFullyUnusedPack(t *testing.T) {
	ctx := context.Background()
	r, _ := openTestRepo(t)
	defer r.Close(ctx)

	// Snapshot 1, then flush so it lands in its own pack.
	s1 := makeSnapshot(t, r, "a.txt", []byte("first pack content here"))
	// Force a fresh pack for the second snapshot by flushing between them
	// (makeSnapshot already flushes). They will typically share a pack only if
	// small; to guarantee a fully-unused pack, delete s1 while s2 is unrelated.
	s2 := makeSnapshot(t, r, "b.txt", []byte("second pack content here"))
	_ = s1

	if err := r.DeleteSnapshot(ctx, s1.ID); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(ctx, r, []*repo.Snapshot{s2})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.DeletePacks) == 0 {
		t.Fatal("expected at least one pack scheduled for deletion")
	}
	if _, err := Execute(ctx, r, plan); err != nil {
		t.Fatal(err)
	}
	// s2 still loadable.
	if _, err := r.LoadTree(ctx, s2.Tree); err != nil {
		t.Fatalf("s2 unusable after prune: %v", err)
	}
}
