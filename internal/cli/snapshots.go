package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func newSnapshotsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "snapshots",
		Short: "List snapshots in the repository",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			r, err := openRepo(ctx, false)
			if err != nil {
				return err
			}
			defer r.Close(ctx)

			snaps, err := r.ListSnapshots(ctx)
			if err != nil {
				return err
			}

			if gf.json {
				type row struct {
					ID       string   `json:"id"`
					Time     string   `json:"time"`
					Hostname string   `json:"hostname"`
					Paths    []string `json:"paths"`
					Tags     []string `json:"tags"`
				}
				rows := make([]row, 0, len(snaps))
				for _, s := range snaps {
					rows = append(rows, row{
						ID:       s.ID,
						Time:     s.Time.Format("2006-01-02T15:04:05Z07:00"),
						Hostname: s.Hostname,
						Paths:    s.Paths,
						Tags:     s.Tags,
					})
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(rows)
			}

			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tTIME\tHOST\tTAGS\tPATHS")
			for _, s := range snaps {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
					short8(s.ID),
					s.Time.Local().Format("2006-01-02 15:04:05"),
					s.Hostname,
					strings.Join(s.Tags, ","),
					strings.Join(s.Paths, ","),
				)
			}
			return tw.Flush()
		},
	}
}
