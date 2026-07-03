package pruner

import (
	"context"
	"fmt"

	"github.com/zephel01/bakku/internal/repo"
)

// Reachable computes the set of blob ids reachable from all snapshots by walking
// every snapshot's tree (recursively) plus every file's content chunk ids. The
// returned set contains both tree blob ids and data blob ids.
func Reachable(ctx context.Context, r *repo.Repository, snaps []*repo.Snapshot) (map[string]struct{}, error) {
	used := make(map[string]struct{})
	// Memoize visited tree blobs so shared subtrees are walked once.
	for _, s := range snaps {
		if s.Tree == "" {
			continue
		}
		if err := walkTree(ctx, r, s.Tree, used); err != nil {
			return nil, fmt.Errorf("pruner: snapshot %s: %w", short8(s.ID), err)
		}
	}
	return used, nil
}

func walkTree(ctx context.Context, r *repo.Repository, treeID string, used map[string]struct{}) error {
	if _, seen := used[treeID]; seen {
		return nil
	}
	used[treeID] = struct{}{} // mark tree blob itself as used

	nodes, err := r.LoadTree(ctx, treeID)
	if err != nil {
		return fmt.Errorf("load tree %s: %w", short8(treeID), err)
	}
	for _, n := range nodes {
		switch n.Type {
		case repo.NodeDir:
			if n.Subtree != "" {
				if err := walkTree(ctx, r, n.Subtree, used); err != nil {
					return err
				}
			}
		case repo.NodeFile:
			for _, c := range n.Content {
				used[c] = struct{}{}
			}
		}
	}
	return nil
}

// PackClass categorises a pack by how its blobs relate to the used set.
type PackClass int

const (
	// PackFullyUsed means every blob in the pack is reachable; keep as-is.
	PackFullyUsed PackClass = iota
	// PackFullyUnused means no blob in the pack is reachable; delete outright.
	PackFullyUnused
	// PackPartial means some blobs are used and some are not; repack the used
	// blobs, then delete the pack.
	PackPartial
)

// PackPlan is the prune decision for one pack.
type PackPlan struct {
	PackID    string
	Class     PackClass
	Size      int64
	UsedBlobs []string // ids of used blobs (for PackPartial repack)
	DeadBytes int64    // approx encrypted bytes of unreferenced blobs
}

// Plan is the full prune plan.
type Plan struct {
	Packs        []PackPlan
	UsedBlobs    map[string]struct{}
	RepackBlobs  map[string]repo.IndexLocation // blobs to move (from partial packs)
	KeepIndex    map[string]repo.IndexLocation // index entries that survive unchanged
	DeletePacks  []string                      // fully-unused + partial (after repack)
	ReclaimBytes int64
}

// BuildPlan analyses the repository and produces a prune plan. It uses the
// on-disk pack headers as the authoritative record of each pack's contents so
// that an index that is missing entries cannot cause live blobs to be dropped.
func BuildPlan(ctx context.Context, r *repo.Repository, snaps []*repo.Snapshot) (*Plan, error) {
	used, err := Reachable(ctx, r, snaps)
	if err != nil {
		return nil, err
	}

	packs, err := r.ListPacks(ctx)
	if err != nil {
		return nil, err
	}

	plan := &Plan{
		UsedBlobs:   used,
		RepackBlobs: make(map[string]repo.IndexLocation),
		KeepIndex:   make(map[string]repo.IndexLocation),
	}

	for _, pi := range packs {
		entries, err := r.ReadPackEntries(ctx, pi.ID)
		if err != nil {
			return nil, err
		}
		var usedIDs []string
		var deadBytes int64
		for _, e := range entries {
			if _, ok := used[e.ID]; ok {
				usedIDs = append(usedIDs, e.ID)
			} else {
				deadBytes += e.Length
			}
		}

		pp := PackPlan{PackID: pi.ID, Size: pi.Size, DeadBytes: deadBytes}
		switch {
		case len(usedIDs) == 0:
			pp.Class = PackFullyUnused
			plan.DeletePacks = append(plan.DeletePacks, pi.ID)
			plan.ReclaimBytes += pi.Size
		case len(usedIDs) == len(entries):
			pp.Class = PackFullyUsed
			// All blobs stay where they are; keep their index entries.
			for _, e := range entries {
				plan.KeepIndex[e.ID] = repo.IndexLocation{
					PackID:          pi.ID,
					Offset:          e.Offset,
					Length:          e.Length,
					Type:            e.Type,
					UncompressedLen: e.UncompressedLen,
				}
			}
		default:
			pp.Class = PackPartial
			pp.UsedBlobs = usedIDs
			for _, e := range entries {
				if _, ok := used[e.ID]; ok {
					plan.RepackBlobs[e.ID] = repo.IndexLocation{
						PackID:          pi.ID,
						Offset:          e.Offset,
						Length:          e.Length,
						Type:            e.Type,
						UncompressedLen: e.UncompressedLen,
					}
				}
			}
			plan.DeletePacks = append(plan.DeletePacks, pi.ID)
			plan.ReclaimBytes += deadBytes
		}
		plan.Packs = append(plan.Packs, pp)
	}
	return plan, nil
}

// Execute carries out a prune plan with a crash-safe ordering:
//
//  1. Repack the used blobs from partial packs into fresh packs (new packs are
//     written and their locations recorded in the new index map).
//  2. Write the NEW index (surviving fully-used entries + repacked entries) and
//     only AFTER it is durably stored delete the OLD index files.
//  3. Delete the now-orphaned packs (fully-unused packs and the source packs of
//     the repack).
//
// Because the new index is written and the old index removed before any pack is
// deleted, a crash at any point leaves the repository with a consistent index
// that still references only live packs. Worst case is some orphaned packs that
// a subsequent prune reclaims.
//
// Stats reports what happened.
type Stats struct {
	PacksDeleted   int
	PacksRepacked  int
	NewPacks       int
	BlobsRepacked  int
	ReclaimedBytes int64
}

func Execute(ctx context.Context, r *repo.Repository, plan *Plan) (*Stats, error) {
	st := &Stats{ReclaimedBytes: plan.ReclaimBytes}

	// Capture the pre-existing index files so we can delete exactly them after
	// the new index is durably written (never the freshly written one).
	oldIndexKeys, err := r.ListIndexFiles(ctx)
	if err != nil {
		return nil, err
	}

	// Build the new index: start from surviving fully-used entries.
	newIndex := make(map[string]repo.IndexLocation, len(plan.KeepIndex)+len(plan.RepackBlobs))
	for id, loc := range plan.KeepIndex {
		newIndex[id] = loc
	}

	// 1. Repack partial-pack blobs into new packs (updates newIndex in place).
	if len(plan.RepackBlobs) > 0 {
		newPacks, err := r.Repack(ctx, plan.RepackBlobs, newIndex)
		if err != nil {
			return nil, fmt.Errorf("pruner: repack: %w", err)
		}
		st.NewPacks = len(newPacks)
		st.BlobsRepacked = len(plan.RepackBlobs)
	}

	// 2. Swap the index: write new, then delete old. This is the safety pivot.
	if err := r.ReplaceIndex(ctx, newIndex, oldIndexKeys); err != nil {
		return nil, fmt.Errorf("pruner: replace index: %w", err)
	}

	// 3. Delete orphaned packs now that no live index references them.
	for _, pid := range plan.DeletePacks {
		if err := r.DeletePack(ctx, pid); err != nil {
			return st, fmt.Errorf("pruner: delete pack %s: %w", short8(pid), err)
		}
		st.PacksDeleted++
	}
	// Count how many of the deleted packs were partial (repacked).
	for _, pp := range plan.Packs {
		if pp.Class == PackPartial {
			st.PacksRepacked++
		}
	}
	return st, nil
}

func short8(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
