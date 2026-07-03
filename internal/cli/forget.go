package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/zephel01/bakku/internal/pruner"
)

func newForgetCmd() *cobra.Command {
	var (
		keepLast    int
		keepDaily   int
		keepWeekly  int
		keepMonthly int
		keepYearly  int
		keepTags    []string
		dryRun      bool
		doPrune     bool
	)

	cmd := &cobra.Command{
		Use:   "forget",
		Short: "Remove snapshots according to a GFS retention policy",
		Long: "Apply a grandfather-father-son retention policy, grouping snapshots by\n" +
			"host+paths and keeping the newest snapshot in each retained time bucket.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			policy := pruner.Policy{
				KeepLast:    keepLast,
				KeepDaily:   keepDaily,
				KeepWeekly:  keepWeekly,
				KeepMonthly: keepMonthly,
				KeepYearly:  keepYearly,
				KeepTags:    keepTags,
			}
			if policy.Empty() {
				return fmt.Errorf("forget: at least one --keep-* option is required")
			}

			r, err := openRepo(ctx, false)
			if err != nil {
				return err
			}
			defer r.Close(ctx)

			snaps, err := r.ListSnapshots(ctx)
			if err != nil {
				return err
			}
			decisions := pruner.ApplyGrouped(snaps, policy)

			var keep, forget []pruner.Decision
			for _, d := range decisions {
				if d.Keep {
					keep = append(keep, d)
				} else {
					forget = append(forget, d)
				}
			}

			if gf.json {
				if err := emitForgetJSON(cmd, keep, forget, dryRun); err != nil {
					return err
				}
			} else {
				printForget(cmd, keep, forget, dryRun)
			}

			if dryRun {
				return nil
			}

			for _, d := range forget {
				if err := r.DeleteSnapshot(ctx, d.Snapshot.ID); err != nil {
					return fmt.Errorf("forget: delete snapshot %s: %w", short8(d.Snapshot.ID), err)
				}
			}

			if doPrune && len(forget) > 0 {
				if !gf.json {
					fmt.Fprintln(cmd.OutOrStdout(), "\nrunning prune...")
				}
				return runPrune(cmd, r, false)
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.IntVar(&keepLast, "keep-last", 0, "keep the N most recent snapshots")
	f.IntVar(&keepDaily, "keep-daily", 0, "keep the newest snapshot for the N most recent days")
	f.IntVar(&keepWeekly, "keep-weekly", 0, "keep the newest snapshot for the N most recent weeks")
	f.IntVar(&keepMonthly, "keep-monthly", 0, "keep the newest snapshot for the N most recent months")
	f.IntVar(&keepYearly, "keep-yearly", 0, "keep the newest snapshot for the N most recent years")
	f.StringSliceVar(&keepTags, "keep-tag", nil, "always keep snapshots carrying any of these tags")
	f.BoolVar(&dryRun, "dry-run", false, "show what would be removed without deleting")
	f.BoolVar(&doPrune, "prune", false, "run prune after forgetting to reclaim space")
	return cmd
}

func printForget(cmd *cobra.Command, keep, forget []pruner.Decision, dryRun bool) {
	w := cmd.OutOrStdout()
	verb := "removing"
	if dryRun {
		verb = "would remove"
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintf(tw, "KEEPING %d snapshot(s):\n", len(keep))
	fmt.Fprintln(tw, "ID\tTIME\tHOST\tREASON")
	for _, d := range keep {
		s := d.Snapshot
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", short8(s.ID),
			s.Time.Local().Format("2006-01-02 15:04:05"), s.Hostname,
			strings.Join(d.Reasons, ","))
	}
	tw.Flush()
	fmt.Fprintf(w, "\n%s %d snapshot(s):\n", verb, len(forget))
	tw2 := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw2, "ID\tTIME\tHOST")
	for _, d := range forget {
		s := d.Snapshot
		fmt.Fprintf(tw2, "%s\t%s\t%s\n", short8(s.ID),
			s.Time.Local().Format("2006-01-02 15:04:05"), s.Hostname)
	}
	tw2.Flush()
}

func emitForgetJSON(cmd *cobra.Command, keep, forget []pruner.Decision, dryRun bool) error {
	type snapRow struct {
		ID      string   `json:"id"`
		Time    string   `json:"time"`
		Host    string   `json:"hostname"`
		Reasons []string `json:"reasons,omitempty"`
	}
	row := func(d pruner.Decision) snapRow {
		return snapRow{
			ID:      d.Snapshot.ID,
			Time:    d.Snapshot.Time.Format("2006-01-02T15:04:05Z07:00"),
			Host:    d.Snapshot.Hostname,
			Reasons: d.Reasons,
		}
	}
	out := struct {
		DryRun bool      `json:"dry_run"`
		Keep   []snapRow `json:"keep"`
		Forget []snapRow `json:"forget"`
	}{DryRun: dryRun}
	for _, d := range keep {
		out.Keep = append(out.Keep, row(d))
	}
	for _, d := range forget {
		out.Forget = append(out.Forget, row(d))
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
