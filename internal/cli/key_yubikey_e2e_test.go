package cli

import (
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// installFakeYkchalresp puts a fake `ykchalresp` on PATH (for the duration of
// the test) that simulates a YubiKey programmed with secretHex, exactly like
// the one in internal/e2e/yubikey_e2e_test.go. Duplicated here (rather than
// exported from internal/e2e, which is a test-only package with its own
// module-internal visibility) to keep the CLI's tests self-contained.
func installFakeYkchalresp(t *testing.T, secretHex string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake ykchalresp shell script requires a POSIX shell")
	}
	if _, err := exec.LookPath("xxd"); err != nil {
		t.Skip("xxd not available; cannot run fake ykchalresp script")
	}
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl not available; cannot run fake ykchalresp script")
	}

	bin := t.TempDir()
	script := `#!/bin/sh
# Fake ykchalresp for tests: usage "-<slot> -x <hexchallenge>".
slot=""
hexchallenge=""
while [ $# -gt 0 ]; do
  case "$1" in
    -x) shift; hexchallenge="$1" ;;
    -[0-9]*) slot="$1" ;;
  esac
  shift
done
if [ -z "$hexchallenge" ]; then
  echo "usage: ykchalresp -<slot> -x <hex>" >&2
  exit 1
fi
printf '%s' "$hexchallenge" | xxd -r -p | openssl dgst -sha1 -mac hmac -macopt hexkey:` + secretHex + ` -r 2>/dev/null | awk '{print $1}'
`
	path := filepath.Join(bin, "ykchalresp")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+origPath)
}

// TestE2EKeyAddYubiKeyCLI drives `key add --yubikey`, `key list`, and the
// password-less `--yubikey` open flag through the real CLI command tree
// (with a fake ykchalresp standing in for hardware), then verifies the
// "insurance" warning/guard around removing the last password slot.
func TestE2EKeyAddYubiKeyCLI(t *testing.T) {
	installFakeYkchalresp(t, hex.EncodeToString([]byte("cli-e2e-secret")))

	dir := t.TempDir()
	repoURL := "file://" + dir

	t.Setenv("BAKKU_PASSWORD", "only-password")
	if out, err := runCLI(t, "--repo", repoURL, "init"); err != nil {
		t.Fatalf("init failed: %v (%s)", err, out)
	}

	// key add --yubikey (opens with the password, adds the yubikey slot).
	out, err := runCLI(t, "--repo", repoURL, "key", "add", "--yubikey")
	if err != nil {
		t.Fatalf("key add --yubikey failed: %v (%s)", err, out)
	}
	if !strings.Contains(out, "added yubikey key slot") {
		t.Fatalf("unexpected output: %s", out)
	}

	// key list must show the yubikey-chalresp type.
	os.Unsetenv("BAKKU_PASSWORD")
	t.Setenv("BAKKU_PASSWORD", "only-password")
	out, err = runCLI(t, "--repo", repoURL, "key", "list")
	if err != nil {
		t.Fatalf("key list failed: %v (%s)", err, out)
	}
	if !strings.Contains(out, "yubikey-chalresp") {
		t.Fatalf("expected yubikey-chalresp in key list output, got: %s", out)
	}

	// Open with --yubikey and NO password at all.
	os.Unsetenv("BAKKU_PASSWORD")
	out, err = runCLI(t, "--repo", repoURL, "--yubikey", "key", "list")
	if err != nil {
		t.Fatalf("--yubikey open failed: %v (%s)", err, out)
	}
	if !strings.Contains(out, "yubikey-chalresp") {
		t.Fatalf("expected yubikey-chalresp in --yubikey key list output, got: %s", out)
	}

	// Removing the only password slot (leaving only the yubikey slot) must be
	// refused without --force.
	t.Setenv("BAKKU_PASSWORD", "only-password")
	out, err = runCLI(t, "--repo", repoURL, "--json", "key", "list")
	if err != nil {
		t.Fatal(err)
	}
	passwordID := extractPasswordSlotID(t, out)

	if _, err := runCLI(t, "--repo", repoURL, "key", "remove", passwordID); err == nil {
		t.Fatal("expected removing the last password slot (yubikey-only left) to be refused without --force")
	}

	// With --force it must succeed.
	out, err = runCLI(t, "--repo", repoURL, "key", "remove", passwordID, "--force")
	if err != nil {
		t.Fatalf("key remove --force failed: %v (%s)", err, out)
	}

	// Now the repository can only be opened via --yubikey.
	os.Unsetenv("BAKKU_PASSWORD")
	if _, err := runCLI(t, "--repo", repoURL, "--yubikey", "key", "list"); err != nil {
		t.Fatalf("yubikey-only open failed: %v", err)
	}
}

func extractPasswordSlotID(t *testing.T, jsonOut string) string {
	t.Helper()
	var infos []struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	if err := decodeJSON(jsonOut, &infos); err != nil {
		t.Fatalf("decode key list json: %v (%s)", err, jsonOut)
	}
	for _, i := range infos {
		if i.Type == "password" {
			return i.ID
		}
	}
	t.Fatalf("no password slot in %s", jsonOut)
	return ""
}
