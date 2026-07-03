package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/zephel01/bakku/internal/config"
	"github.com/zephel01/bakku/internal/repo"
	"github.com/zephel01/bakku/internal/yubikey"
)

func newKeyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "key",
		Short: "Manage repository key slots (multiple passwords can open the same repo)",
		Long: "Manage repository key slots. Every slot wraps the same repository master\n" +
			"key with its own password, so losing one key still lets you open the\n" +
			"repository with another. The last remaining slot cannot be removed.",
	}
	cmd.AddCommand(newKeyAddCmd(), newKeyListCmd(), newKeyRemoveCmd())
	return cmd
}

// keyAdd flags.
var (
	keyAddNewPasswordFile string
	keyAddYubiKey         bool
	keyAddYubiKeySlot     int
	keyRemoveForce        bool
)

func newKeyAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a new password or YubiKey key slot to the repository",
		Long: "Open the repository with an existing credential, then add a new key\n" +
			"slot.\n\n" +
			"Password slot (default): the new password is taken from\n" +
			"BAKKU_NEW_PASSWORD, or --new-password-file, or an interactive prompt\n" +
			"(entered twice).\n\n" +
			"YubiKey slot (--yubikey): challenges the YubiKey twice via ykchalresp\n" +
			"or ykman (whichever is on PATH) to confirm a stable HMAC-SHA1\n" +
			"response, then wraps the master key with a key derived from that\n" +
			"response. See docs/quickguide.md for setup (`ykman otp chalresp\n" +
			"--generate 2`).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			r, err := openRepo(ctx, false)
			if err != nil {
				return err
			}
			defer r.Close(ctx)

			if keyAddYubiKey {
				slot := keyAddYubiKeySlot
				if slot == 0 {
					slot = gf.yubikeySlot
				}
				id, err := r.AddYubiKeySlot(ctx, yubikey.NewExecResponder(), slot)
				if err != nil {
					return err
				}
				if gf.json {
					return emitJSON(cmd, map[string]any{"added": id, "type": string(repo.KeyTypeYubiKey)})
				}
				fmt.Fprintf(cmd.OutOrStdout(), "added yubikey key slot %s\n", short12(id))
				warnIfNoPasswordSlot(cmd, r)
				return nil
			}

			newPW, err := resolveNewPassword(keyAddNewPasswordFile)
			if err != nil {
				return err
			}
			id, err := r.AddPasswordKeySlot(ctx, newPW)
			if err != nil {
				return err
			}
			if gf.json {
				return emitJSON(cmd, map[string]any{"added": id, "type": string(repo.KeyTypePassword)})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "added key slot %s\n", short12(id))
			return nil
		},
	}
	cmd.Flags().StringVar(&keyAddNewPasswordFile, "new-password-file", "", "read the new slot's password from this file")
	cmd.Flags().BoolVar(&keyAddYubiKey, "yubikey", false, "add a YubiKey challenge-response slot instead of a password")
	cmd.Flags().IntVar(&keyAddYubiKeySlot, "yubikey-slot", 0, "YubiKey OTP slot to register (default 2)")
	return cmd
}

// warnIfNoPasswordSlot prints a strong recommendation to stderr (not an
// error) when, after the just-added YubiKey slot, the repository has no
// remaining password slot at all. Losing the only YubiKey then makes the
// repository permanently unrecoverable, so this is surfaced every time a
// yubikey-only state is reached, even though it is allowed.
// wouldLosePasswordInsurance reports whether removing the slot identified by
// idPrefix would leave slots with zero password-type entries while at least
// one yubikey-chalresp entry remains (i.e. the repository becomes
// recoverable only via hardware afterward). If idPrefix does not resolve to
// exactly one slot, or removal would not create this situation, it returns
// false (RemoveKeySlot itself handles ambiguous/missing ids).
func wouldLosePasswordInsurance(slots []repo.KeySlotInfo, idPrefix string) bool {
	var target *repo.KeySlotInfo
	for i := range slots {
		if hasIDPrefix(slots[i].ID, idPrefix) {
			if target != nil {
				return false // ambiguous; let RemoveKeySlot report the real error
			}
			target = &slots[i]
		}
	}
	if target == nil || target.Type != repo.KeyTypePassword {
		return false // removing a non-password slot never reduces password coverage
	}
	remainingPasswords, remainingYubiKeys := 0, 0
	for _, s := range slots {
		if s.ID == target.ID {
			continue
		}
		switch s.Type {
		case repo.KeyTypePassword:
			remainingPasswords++
		case repo.KeyTypeYubiKey:
			remainingYubiKeys++
		}
	}
	return remainingPasswords == 0 && remainingYubiKeys > 0
}

func warnIfNoPasswordSlot(cmd *cobra.Command, r *repo.Repository) {
	slots, err := r.ListKeySlots(cmd.Context())
	if err != nil {
		return
	}
	hasPassword := false
	for _, s := range slots {
		if s.Type == repo.KeyTypePassword {
			hasPassword = true
			break
		}
	}
	if !hasPassword {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning: this repository now has no password key slot; if this YubiKey is lost or damaged, the repository cannot be recovered. Strongly recommended: keep at least one password slot (`bakku key add`) and/or register a second, backup YubiKey (`bakku key add --yubikey`).")
	}
}

func newKeyListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List key slots",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			r, err := openRepo(ctx, false)
			if err != nil {
				return err
			}
			defer r.Close(ctx)

			slots, err := r.ListKeySlots(ctx)
			if err != nil {
				return err
			}
			if gf.json {
				return emitJSON(cmd, slots)
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tTYPE\tCREATED\tCURRENT")
			for _, s := range slots {
				cur := ""
				if s.Current {
					cur = "*"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", short12(s.ID), s.Type, s.Created.Local().Format(time.RFC3339), cur)
			}
			return tw.Flush()
		},
	}
}

func newKeyRemoveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove <keyID>",
		Short: "Remove a key slot (refuses to remove the last one)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			r, err := openRepo(ctx, false)
			if err != nil {
				return err
			}
			defer r.Close(ctx)

			// Guard: warn (and require --force) when removing the slot we just
			// used to open the repo. Determine this before deleting.
			slots, err := r.ListKeySlots(ctx)
			if err != nil {
				return err
			}
			for _, s := range slots {
				if s.Current && hasIDPrefix(s.ID, args[0]) && !keyRemoveForce {
					return fmt.Errorf("refusing to remove the key slot currently in use; re-run with --force to confirm")
				}
			}

			// Guard: warn (and require --force) when this removal would leave the
			// repository with password slots at zero while yubikey slots remain
			// (i.e. the repo becomes recoverable only via hardware). This mirrors
			// the "insurance" warning shown by `key add --yubikey`.
			if wouldLosePasswordInsurance(slots, args[0]) && !keyRemoveForce {
				return fmt.Errorf("removing this slot would leave the repository with no password slot (only YubiKey); " +
					"losing the YubiKey would then make the repository unrecoverable. Strongly recommended to keep at " +
					"least one password slot. Re-run with --force to proceed anyway")
			}

			removed, wasCurrent, err := r.RemoveKeySlot(ctx, args[0])
			if err != nil {
				if errors.Is(err, repo.ErrLastKeySlot) {
					return fmt.Errorf("%w (a repository must keep at least one key)", err)
				}
				return err
			}
			if gf.json {
				return emitJSON(cmd, map[string]any{"removed": removed, "was_current": wasCurrent})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed key slot %s\n", short12(removed))
			return nil
		},
	}
	cmd.Flags().BoolVar(&keyRemoveForce, "force", false, "allow removing the key slot currently in use")
	return cmd
}

// resolveNewPassword obtains the new key slot's password. Precedence:
// BAKKU_NEW_PASSWORD -> --new-password-file -> interactive (twice).
func resolveNewPassword(file string) ([]byte, error) {
	if p := os.Getenv("BAKKU_NEW_PASSWORD"); p != "" {
		return []byte(p), nil
	}
	if file != "" {
		return config.ReadPasswordFile(file)
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return nil, errors.New("no new password provided (set BAKKU_NEW_PASSWORD, use --new-password-file, or run interactively)")
	}
	fmt.Fprint(os.Stderr, "enter new key password: ")
	pw, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return nil, err
	}
	if len(pw) == 0 {
		return nil, errors.New("empty password")
	}
	fmt.Fprint(os.Stderr, "confirm new key password: ")
	pw2, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return nil, err
	}
	if string(pw) != string(pw2) {
		return nil, errors.New("passwords do not match")
	}
	return pw, nil
}

// short12 abbreviates a hex id to 12 chars for display.
func short12(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// hasIDPrefix reports whether full has the given prefix.
func hasIDPrefix(full, prefix string) bool {
	return len(full) >= len(prefix) && full[:len(prefix)] == prefix
}

// emitJSON writes v as indented JSON to the command's stdout.
func emitJSON(cmd *cobra.Command, v any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
