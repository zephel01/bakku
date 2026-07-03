package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zephel01/bakku/internal/repo"
	"github.com/zephel01/bakku/internal/restorer"
)

func newLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls <snapshot-id>",
		Short: "List the files in a snapshot",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			r, err := openRepo(ctx, false)
			if err != nil {
				return err
			}
			defer r.Close(ctx)

			snap, err := r.FindSnapshot(ctx, args[0])
			if err != nil {
				return err
			}
			rs := restorer.New(r)
			w := cmd.OutOrStdout()
			return rs.Walk(ctx, snap, func(path string, n repo.Node) error {
				marker := ""
				switch n.Type {
				case repo.NodeDir:
					marker = "/"
				case repo.NodeSymlink:
					fmt.Fprintf(w, "%s -> %s\n", path, n.LinkTarget)
					return nil
				}
				fmt.Fprintf(w, "%s%s\n", path, marker)
				return nil
			})
		},
	}
}
