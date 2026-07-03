package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/zephel01/bakku/internal/notify"
	"github.com/zephel01/bakku/internal/pruner"
	"github.com/zephel01/bakku/internal/repo"
)

func newPruneCmd() *cobra.Command {
	var dryRun bool
	var noNotify bool

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Reclaim space by removing unreferenced data",
		Long: "Compute the set of blobs reachable from all snapshots, then delete packs\n" +
			"that hold no live blobs and repack packs that are only partially used.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			start := time.Now()
			notifier, notifyEnabled := newNotifier(noNotify)

			r, err := openRepo(ctx, false)
			if err != nil {
				notifyPruneResult(ctx, notifier, notifyEnabled, nil, start, err)
				return err
			}
			defer r.Close(ctx)
			err = runPrune(cmd, r, dryRun)
			notifyPruneResult(ctx, notifier, notifyEnabled, nil, start, err)
			return err
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be reclaimed without modifying the repository")
	cmd.Flags().BoolVar(&noNotify, "no-notify", false, "skip sending the configured webhook notification for this run")
	return cmd
}

// notifyPruneResult sends a best-effort notify.Event summarizing a prune run.
// stats may be nil (kept as a parameter for symmetry/future use; today the
// event carries no stats payload beyond success/failure and error).
func notifyPruneResult(ctx context.Context, n *notify.Notifier, enabled bool, stats map[string]any, start time.Time, runErr error) {
	if !enabled || n == nil {
		return
	}
	ev := notify.Event{
		Job:      "prune",
		Hostname: hostnameOrUnknown(),
		Stats:    stats,
		Duration: time.Since(start),
		Time:     time.Now().UTC(),
	}
	if runErr != nil {
		ev.Status = "failure"
		ev.Error = runErr.Error()
	} else {
		ev.Status = "success"
	}
	if err := n.Send(ctx, ev); err != nil {
		fmt.Fprintf(os.Stderr, "warning: notify: %v\n", err)
	}
}

// runPrune builds and (unless dryRun) executes a prune plan on an already-open
// repository. Shared by `bakku prune` and `bakku forget --prune`.
func runPrune(cmd *cobra.Command, r *repo.Repository, dryRun bool) error {
	ctx := cmd.Context()
	snaps, err := r.ListSnapshots(ctx)
	if err != nil {
		return err
	}
	plan, err := pruner.BuildPlan(ctx, r, snaps)
	if err != nil {
		return err
	}

	var fullyUnused, partial, fullyUsed int
	for _, pp := range plan.Packs {
		switch pp.Class {
		case pruner.PackFullyUnused:
			fullyUnused++
		case pruner.PackPartial:
			partial++
		case pruner.PackFullyUsed:
			fullyUsed++
		}
	}

	if gf.json {
		out := struct {
			DryRun       bool   `json:"dry_run"`
			Snapshots    int    `json:"snapshots"`
			Packs        int    `json:"packs_total"`
			PacksKeep    int    `json:"packs_keep"`
			PacksDelete  int    `json:"packs_delete"`
			PacksRepack  int    `json:"packs_repack"`
			UsedBlobs    int    `json:"used_blobs"`
			RepackBlobs  int    `json:"repack_blobs"`
			ReclaimBytes int64  `json:"reclaim_bytes"`
			Reclaimed    bool   `json:"reclaimed"`
			NewPacks     int    `json:"new_packs,omitempty"`
			Message      string `json:"message,omitempty"`
		}{
			DryRun:       dryRun,
			Snapshots:    len(snaps),
			Packs:        len(plan.Packs),
			PacksKeep:    fullyUsed,
			PacksDelete:  fullyUnused,
			PacksRepack:  partial,
			UsedBlobs:    len(plan.UsedBlobs),
			RepackBlobs:  len(plan.RepackBlobs),
			ReclaimBytes: plan.ReclaimBytes,
		}
		if !dryRun {
			st, err := pruner.Execute(ctx, r, plan)
			if err != nil {
				return err
			}
			out.Reclaimed = true
			out.NewPacks = st.NewPacks
			out.ReclaimBytes = st.ReclaimedBytes
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "snapshots:        %d\n", len(snaps))
	fmt.Fprintf(w, "packs:            %d total\n", len(plan.Packs))
	fmt.Fprintf(w, "  fully used:     %d (keep)\n", fullyUsed)
	fmt.Fprintf(w, "  partially used: %d (repack)\n", partial)
	fmt.Fprintf(w, "  fully unused:   %d (delete)\n", fullyUnused)
	fmt.Fprintf(w, "used blobs:       %d\n", len(plan.UsedBlobs))
	fmt.Fprintf(w, "blobs to repack:  %d\n", len(plan.RepackBlobs))
	fmt.Fprintf(w, "reclaimable:      %s\n", humanBytes(plan.ReclaimBytes))

	if dryRun {
		fmt.Fprintln(w, "\n(dry-run: no changes made)")
		return nil
	}

	if fullyUnused == 0 && partial == 0 {
		fmt.Fprintln(w, "\nnothing to prune.")
		return nil
	}

	st, err := pruner.Execute(ctx, r, plan)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "\ndone: deleted %d pack(s), repacked %d blob(s) into %d new pack(s), reclaimed %s\n",
		st.PacksDeleted, st.BlobsRepacked, st.NewPacks, humanBytes(st.ReclaimedBytes))
	return nil
}
