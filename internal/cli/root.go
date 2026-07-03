// Package cli defines bakku's cobra command tree.
package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zephel01/bakku/internal/backend"
	"github.com/zephel01/bakku/internal/config"
	"github.com/zephel01/bakku/internal/repo"
)

// globalFlags holds flags shared across commands.
type globalFlags struct {
	repo         string
	configPath   string
	passwordFile string
	json         bool
}

var gf globalFlags

// BuildInfo carries version metadata injected by `main` (set via -ldflags at
// release-build time; see scripts/build-release.sh).
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

// String renders build info as bakku's standard `--version`/`version` line.
func (b BuildInfo) String() string {
	return fmt.Sprintf("bakku %s (commit %s, built %s)", b.Version, b.Commit, b.Date)
}

// NewRootCmd builds the root command tree.
func NewRootCmd(info BuildInfo) *cobra.Command {
	root := &cobra.Command{
		Use:           "bakku",
		Short:         "bakku - a cross-platform, encrypted, deduplicating backup tool",
		Version:       info.String(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetVersionTemplate("{{.Version}}\n")
	pf := root.PersistentFlags()
	pf.StringVar(&gf.repo, "repo", "", "destination name (from config) or backend URL")
	pf.StringVar(&gf.configPath, "config", "", "config file path (default: $BAKKU_CONFIG or ~/.config/bakku/config.toml)")
	pf.StringVar(&gf.passwordFile, "password-file", "", "read repository password from this file")
	pf.BoolVar(&gf.json, "json", false, "emit machine-readable JSON output where supported")

	root.AddCommand(
		newInitCmd(),
		newBackupCmd(),
		newSnapshotsCmd(),
		newRestoreCmd(),
		newLsCmd(),
		newDestCmd(),
		newForgetCmd(),
		newPruneCmd(),
		newCheckCmd(),
		newDiffCmd(),
		newVerifyRestoreCmd(),
		newScheduleCmd(),
		newVersionCmd(info),
	)
	return root
}

// newVersionCmd adds an explicit `bakku version` subcommand alongside the
// cobra-provided `--version`/`-v` flag (both print the same string).
func newVersionCmd(info BuildInfo) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the bakku version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), info.String())
			return nil
		},
	}
}

// loadConfig loads the config honoring the global --config flag.
func loadConfig() (*config.Config, error) {
	return config.Load(gf.configPath)
}

// resolveBackendURL turns the --repo flag into a backend URL via config.
func resolveBackendURL() (string, error) {
	cfg, err := loadConfig()
	if err != nil {
		return "", err
	}
	return cfg.ResolveDest(gf.repo)
}

// openBackend resolves --repo and opens the backend.
func openBackend(ctx context.Context) (backend.Backend, error) {
	url, err := resolveBackendURL()
	if err != nil {
		return nil, err
	}
	return backend.Open(ctx, url, backend.Options{})
}

// openRepo opens the backend + repository, prompting/reading the password.
func openRepo(ctx context.Context, confirm bool) (*repo.Repository, error) {
	be, err := openBackend(ctx)
	if err != nil {
		return nil, err
	}
	pw, err := config.ResolvePassword(config.PasswordOptions{File: gf.passwordFile, Confirm: confirm})
	if err != nil {
		be.Close()
		return nil, err
	}
	r, err := repo.Open(ctx, be, pw)
	if err != nil {
		be.Close()
		return nil, err
	}
	return r, nil
}
