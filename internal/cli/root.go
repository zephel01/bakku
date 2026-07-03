// Package cli defines bakku's cobra command tree.
package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zephel01/bakku/internal/backend"
	"github.com/zephel01/bakku/internal/config"
	"github.com/zephel01/bakku/internal/repo"
	"github.com/zephel01/bakku/internal/yubikey"
)

// globalFlags holds flags shared across commands.
type globalFlags struct {
	repo            string
	configPath      string
	passwordFile    string
	passwordCommand string
	json            bool
	yubikey         bool
	yubikeySlot     int
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
// Fields still at their unknown defaults are omitted rather than printed as
// "none"/"unknown" (e.g. module builds via `go install` have a version but no
// VCS metadata).
func (b BuildInfo) String() string {
	s := "bakku " + b.Version
	switch {
	case b.Commit != noCommit && b.Date != unknownDate:
		s += fmt.Sprintf(" (commit %s, built %s)", b.Commit, b.Date)
	case b.Commit != noCommit:
		s += fmt.Sprintf(" (commit %s)", b.Commit)
	case b.Date != unknownDate:
		s += fmt.Sprintf(" (built %s)", b.Date)
	}
	return s
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
	pf.StringVar(&gf.passwordCommand, "password-command", "", "run this shell command; its first stdout line is the password (e.g. \"op read op://Private/bakku/password\")")
	pf.BoolVar(&gf.json, "json", false, "emit machine-readable JSON output where supported")
	pf.BoolVar(&gf.yubikey, "yubikey", false, "unlock the repository using a registered YubiKey challenge-response slot instead of a password")
	pf.IntVar(&gf.yubikeySlot, "yubikey-slot", 0, "OTP slot to use with --yubikey on `key add` (default: repository's stored slot when opening; 2 when adding)")

	root.AddCommand(
		newInitCmd(),
		newBackupCmd(),
		newSnapshotsCmd(),
		newRestoreCmd(),
		newLsCmd(),
		newDestCmd(),
		newKeyCmd(),
		newPasswordCmd(),
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

// passwordOptions builds config.PasswordOptions honoring the global flags plus
// config-derived password_command and OS-keychain lookup for the current --repo.
// confirm only affects the interactive prompt (used by `init`).
func passwordOptions(confirm bool) config.PasswordOptions {
	opts := config.PasswordOptions{
		File:    gf.passwordFile,
		Command: gf.passwordCommand,
		Confirm: confirm,
	}
	cfg, err := loadConfig()
	if err == nil {
		resolved, rerr := cfg.ResolveDest(gf.repo)
		if rerr == nil {
			opts.ConfigCommand = cfg.DestPasswordCommand(gf.repo, resolved)
			opts.KeychainRepo = resolved
		}
	}
	return opts
}

// openRepo opens the backend + repository, prompting/reading the password.
//
// YubiKey handling:
//   - --yubikey: skip password resolution entirely and unlock with a
//     registered yubikey-chalresp slot (via whichever external tool -
//     ykchalresp or ykman - is found on PATH).
//   - no --yubikey: resolve the password normally. If that fails (all
//     password sources exhausted / no interactive terminal) AND the
//     repository has at least one yubikey-chalresp slot AND a YubiKey tool is
//     on PATH, silently retry with YubiKey unlock before giving up. This lets
//     a scheduled/headless job that only has a YubiKey plugged in still work
//     without requiring --yubikey explicitly.
func openRepo(ctx context.Context, confirm bool) (*repo.Repository, error) {
	be, err := openBackend(ctx)
	if err != nil {
		return nil, err
	}

	if gf.yubikey {
		r, yerr := repo.OpenWithYubiKey(ctx, be, yubikey.NewExecResponder())
		if yerr != nil {
			be.Close()
			return nil, fmt.Errorf("yubikey unlock failed: %w", yerr)
		}
		return r, nil
	}

	pw, pwErr := config.ResolvePassword(passwordOptions(confirm))
	if pwErr != nil {
		// Auto-fallback: only attempt this if the repo actually has a YubiKey
		// slot and a tool is available, so the common (no YubiKey) case never
		// pays for a wasted backend round-trip or a confusing extra prompt.
		if yubikey.DetectAvailable() {
			hasSlot, hErr := repo.HasYubiKeySlot(ctx, be)
			if hErr == nil && hasSlot {
				r, yerr := repo.OpenWithYubiKey(ctx, be, yubikey.NewExecResponder())
				if yerr == nil {
					return r, nil
				}
			}
		}
		be.Close()
		return nil, pwErr
	}

	r, err := repo.Open(ctx, be, pw)
	if err != nil {
		be.Close()
		return nil, err
	}
	return r, nil
}
