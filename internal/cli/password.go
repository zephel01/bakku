package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zephel01/bakku/internal/config"
	"github.com/zephel01/bakku/internal/keychain"
)

func newPasswordCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "password",
		Short: "Store or remove the repository password in the OS keychain",
		Long: "Store the repository password in the OS secret store (macOS Keychain,\n" +
			"Windows Credential Manager, or Linux Secret Service) so subsequent\n" +
			"commands can open --repo without prompting. On headless systems where no\n" +
			"secret service is available, storing fails; the password resolution chain\n" +
			"then simply falls through to the interactive prompt.",
	}
	cmd.AddCommand(newPasswordStoreCmd(), newPasswordForgetCmd())
	return cmd
}

func newPasswordStoreCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "store",
		Short: "Store the password for --repo in the OS keychain",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved, err := resolveBackendURL()
			if err != nil {
				return err
			}
			// Obtain the password via the normal chain but skip the keychain
			// lookup itself (we are populating it). This lets the user pipe it in
			// from a file/command or type it interactively.
			opts := passwordOptions(false)
			opts.KeychainRepo = "" // do not read from keychain when storing
			pw, err := config.ResolvePassword(opts)
			if err != nil {
				return err
			}
			if err := keychain.Set(resolved, pw); err != nil {
				return fmt.Errorf("store password: %w", err)
			}
			if gf.json {
				return emitJSON(cmd, map[string]any{"stored": resolved})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "stored password for %s in the OS keychain\n", resolved)
			return nil
		},
	}
}

func newPasswordForgetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "forget",
		Short: "Remove the stored password for --repo from the OS keychain",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved, err := resolveBackendURL()
			if err != nil {
				return err
			}
			err = keychain.Delete(resolved)
			if err != nil && !errors.Is(err, keychain.ErrNotFound) {
				return fmt.Errorf("forget password: %w", err)
			}
			existed := err == nil
			if gf.json {
				return emitJSON(cmd, map[string]any{"forgot": resolved, "existed": existed})
			}
			if existed {
				fmt.Fprintf(cmd.OutOrStdout(), "removed stored password for %s\n", resolved)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "no stored password for %s\n", resolved)
			}
			return nil
		},
	}
}
