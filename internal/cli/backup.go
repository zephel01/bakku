package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/zephel01/bakku/internal/archiver"
	bfs "github.com/zephel01/bakku/internal/fs"
	"github.com/zephel01/bakku/internal/notify"
)

func newBackupCmd() *cobra.Command {
	var tags []string
	var excludes []string
	var parallel int
	var useVSS bool
	var noNotify bool

	cmd := &cobra.Command{
		Use:   "backup <paths...>",
		Short: "Back up one or more paths to the repository (incremental)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			start := time.Now()

			if useVSS {
				if err := bfs.UseVSS(); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: %v (continuing without a volume snapshot)\n", err)
				}
			}

			notifier, notifyEnabled := newNotifier(noNotify)

			r, err := openRepo(ctx, false)
			if err != nil {
				notifyBackupResult(ctx, notifier, notifyEnabled, "", nil, start, err)
				return err
			}
			defer r.Close(ctx)

			a := archiver.New(r)
			id, stats, err := a.Backup(ctx, archiver.Options{
				Paths:    args,
				Tags:     tags,
				Excludes: excludes,
				Parallel: parallel,
			})
			notifyBackupResult(ctx, notifier, notifyEnabled, id, stats, start, err)
			if err != nil {
				return err
			}

			if gf.json {
				out := map[string]any{
					"snapshot":      id,
					"files_new":     stats.FilesNew,
					"dirs":          stats.Dirs,
					"symlinks":      stats.Symlinks,
					"chunks_new":    stats.ChunksNew,
					"chunks_reused": stats.ChunksReused,
					"bytes_total":   stats.BytesTotal,
					"bytes_new":     stats.BytesNew,
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}

			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "snapshot %s created\n", short8(id))
			fmt.Fprintf(w, "  files:   %d\n", stats.FilesNew)
			fmt.Fprintf(w, "  dirs:    %d\n", stats.Dirs)
			fmt.Fprintf(w, "  chunks:  %d new, %d reused\n", stats.ChunksNew, stats.ChunksReused)
			fmt.Fprintf(w, "  data:    %s total, %s new\n", humanBytes(stats.BytesTotal), humanBytes(stats.BytesNew))
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&tags, "tag", nil, "tag(s) to attach to the snapshot")
	cmd.Flags().StringSliceVar(&excludes, "exclude", nil, "glob pattern(s) to exclude")
	cmd.Flags().IntVar(&parallel, "parallel", 0, "worker goroutines (0 = default)")
	cmd.Flags().BoolVar(&useVSS, "use-vss", false, "attempt to use a volume shadow copy on Windows (not yet implemented; warns and continues without one)")
	cmd.Flags().BoolVar(&noNotify, "no-notify", false, "skip sending the configured webhook notification for this run")
	return cmd
}

// notifyBackupResult sends a best-effort notify.Event summarizing a backup
// run. Delivery/formatting failures are only ever printed as warnings to
// stderr; they never affect the backup command's own exit status.
func notifyBackupResult(ctx context.Context, n *notify.Notifier, enabled bool, snapshotID string, stats *archiver.Stats, start time.Time, runErr error) {
	if !enabled || n == nil {
		return
	}
	ev := notify.Event{
		Job:        "backup",
		Hostname:   hostnameOrUnknown(),
		SnapshotID: snapshotID,
		Duration:   time.Since(start),
		Time:       time.Now().UTC(),
	}
	if runErr != nil {
		ev.Status = "failure"
		ev.Error = runErr.Error()
	} else {
		ev.Status = "success"
		if stats != nil {
			ev.Stats = map[string]any{
				"files_new":     stats.FilesNew,
				"dirs":          stats.Dirs,
				"symlinks":      stats.Symlinks,
				"chunks_new":    stats.ChunksNew,
				"chunks_reused": stats.ChunksReused,
				"bytes_total":   stats.BytesTotal,
				"bytes_new":     stats.BytesNew,
			}
		}
	}
	if err := n.Send(ctx, ev); err != nil {
		fmt.Fprintf(os.Stderr, "warning: notify: %v\n", err)
	}
}

func hostnameOrUnknown() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "unknown"
	}
	return h
}

func short8(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n/div >= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
