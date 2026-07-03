package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func newDestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dest",
		Short: "Manage backup destinations (name -> URL) in the config file",
	}
	cmd.AddCommand(newDestAddCmd(), newDestListCmd(), newDestRemoveCmd())
	return cmd
}

func newDestAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <name> <url>",
		Short: "Add or update a destination",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			cfg.AddDest(args[0], args[1])
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "added dest %q -> %s (%s)\n", args[0], args[1], cfg.Path())
			return nil
		},
	}
}

func newDestListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured destinations",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if len(cfg.Dests) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no destinations configured")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tURL")
			for _, d := range cfg.Dests {
				fmt.Fprintf(tw, "%s\t%s\n", d.Name, d.URL)
			}
			return tw.Flush()
		},
	}
}

func newDestRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a destination",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if !cfg.RemoveDest(args[0]) {
				return fmt.Errorf("no dest named %q", args[0])
			}
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed dest %q\n", args[0])
			return nil
		},
	}
}
