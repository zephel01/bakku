package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zephel01/bakku/internal/config"
	"github.com/zephel01/bakku/internal/repo"
)

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create a new repository at --repo",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			be, err := openBackend(ctx)
			if err != nil {
				return err
			}
			defer be.Close()

			pw, err := config.ResolvePassword(passwordOptions(true))
			if err != nil {
				return err
			}
			r, err := repo.Init(ctx, be, pw)
			if err != nil {
				return err
			}
			id, _ := r.Config()
			if err := r.Close(ctx); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "initialized repository %s\n", id)
			return nil
		},
	}
}
