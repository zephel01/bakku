package cli

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/zephel01/bakku/internal/scheduler"
)

func newScheduleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schedule",
		Short: "Install, remove, or inspect OS-native scheduled bakku jobs",
		Long: "Manage recurring bakku invocations using the native scheduler of the\n" +
			"current OS: systemd --user timers on Linux, launchd agents on macOS, and\n" +
			"Scheduled Tasks (schtasks.exe) on Windows.",
	}
	cmd.AddCommand(newScheduleInstallCmd(), newScheduleUninstallCmd(), newScheduleStatusCmd())
	return cmd
}

func newScheduleInstallCmd() *cobra.Command {
	var name string
	var cronExpr string

	cmd := &cobra.Command{
		Use:   "install --name <job> --cron \"<cron-expr>\" -- <bakku-subcommand> [args...]",
		Short: "Install a recurring bakku job",
		Long: "Registers a native scheduled job (systemd timer / launchd agent /\n" +
			"Windows Scheduled Task) that runs `bakku <args>` on the given cron\n" +
			"schedule. Everything after a literal \"--\" is passed as the command to\n" +
			"run, e.g.:\n\n" +
			"  bakku schedule install --name daily-docs --cron \"0 3 * * *\" -- backup ~/Documents --repo laptop\n",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			if cronExpr == "" {
				return fmt.Errorf("--cron is required")
			}
			if len(args) == 0 {
				return fmt.Errorf("no command given; pass the bakku subcommand to run after \"--\", e.g. -- backup ~/Documents --repo laptop")
			}
			if err := scheduler.ValidateCron(cronExpr); err != nil {
				return err
			}
			exe, err := os.Executable()
			if err != nil {
				return fmt.Errorf("resolve bakku executable path: %w", err)
			}
			job := scheduler.Job{
				Name:      name,
				CronExpr:  cronExpr,
				Command:   args,
				BakkuPath: exe,
			}
			if err := scheduler.Install(job); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "installed job %q (cron %q -> bakku %v)\n", name, cronExpr, args)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "unique job name (letters, digits, - and _)")
	cmd.Flags().StringVar(&cronExpr, "cron", "", `standard 5-field cron expression, e.g. "0 3 * * *"`)
	return cmd
}

func newScheduleUninstallCmd() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "uninstall --name <job>",
		Short: "Remove a previously installed scheduled job",
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			if err := scheduler.Uninstall(name); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "uninstalled job %q\n", name)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "job name to remove")
	return cmd
}

func newScheduleStatusCmd() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "status [--name <job>]",
		Short: "Show installed scheduled job(s)",
		Long:  "With --name, shows the status of a single job. Without it, lists every bakku-managed scheduled job found on this system.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if name != "" {
				st, err := scheduler.StatusOne(name)
				if err != nil {
					return err
				}
				return printScheduleStatus(cmd, []scheduler.Status{st})
			}
			list, err := scheduler.List()
			if err != nil {
				return err
			}
			if len(list) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no scheduled jobs installed")
				return nil
			}
			return printScheduleStatus(cmd, list)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "show only this job")
	return cmd
}

func printScheduleStatus(cmd *cobra.Command, list []scheduler.Status) error {
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tINSTALLED\tENABLED\tDETAIL")
	for _, st := range list {
		fmt.Fprintf(tw, "%s\t%v\t%v\t%s\n", st.Name, st.Installed, st.Enabled, st.Detail)
	}
	return tw.Flush()
}
