package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zephel01/bakku/internal/restorer"
)

func newRestoreCmd() *cobra.Command {
	var target string
	var includes []string
	var chown bool
	var restoreQuarantine bool

	cmd := &cobra.Command{
		Use:   "restore <snapshot-id>",
		Short: "Restore a snapshot into a target directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if target == "" {
				return fmt.Errorf("--target is required")
			}
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
			if err := rs.Restore(ctx, snap, restorer.Options{
				Target:            target,
				Includes:          includes,
				Chown:             chown,
				RestoreQuarantine: restoreQuarantine,
			}); err != nil {
				return err
			}
			for _, w := range rs.Warnings() {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", w)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "restored snapshot %s to %s\n", short8(snap.ID), target)
			return nil
		},
	}
	cmd.Flags().StringVar(&target, "target", "", "directory to restore into (required)")
	cmd.Flags().StringSliceVar(&includes, "include", nil, "glob pattern(s); only matching files are restored")
	cmd.Flags().BoolVar(&chown, "chown", false, "restore file ownership (uid/gid); requires root/Administrator, ignored otherwise")
	cmd.Flags().BoolVar(&restoreQuarantine, "restore-quarantine", false, "also restore the macOS com.apple.quarantine extended attribute (excluded by default)")
	return cmd
}
