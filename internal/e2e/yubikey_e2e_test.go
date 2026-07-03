package e2e

import (
	"context"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/zephel01/bakku/internal/backend/local"
	"github.com/zephel01/bakku/internal/repo"
	"github.com/zephel01/bakku/internal/yubikey"
)

// writeFakeYkchalresp installs a fake `ykchalresp` shell script on PATH (by
// prepending a temp bin dir to $PATH for the duration of the test) that
// simulates a real YubiKey programmed with secretHex: it parses the
// "-<slot> -x <hexchallenge>" argv ykchalresp uses, computes
// HMAC-SHA1(secret, challenge) and prints the 40-char hex response to stdout,
// exactly like the real tool. This exercises bakku's actual external-command
// exec path (internal/yubikey.execResponder + parseResponse), not just the
// mock Responder used by internal/repo's unit tests.
func writeFakeYkchalresp(t *testing.T, secretHex string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake ykchalresp shell script requires a POSIX shell")
	}
	bin := t.TempDir()
	script := `#!/bin/sh
# Fake ykchalresp for tests: usage "-<slot> -x <hexchallenge>".
# Emits HMAC-SHA1(secret, challenge) as lowercase hex, like the real tool.
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
	// Verify the host actually has xxd + openssl; if not, this environment
	// cannot run the fake tool meaningfully, so skip rather than false-fail.
	if _, err := exec.LookPath("xxd"); err != nil {
		t.Skip("xxd not available; cannot run fake ykchalresp script")
	}
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl not available; cannot run fake ykchalresp script")
	}

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+origPath)
}

func TestE2EYubiKeyPasswordlessUnlockViaFakeExternalTool(t *testing.T) {
	ctx := context.Background()
	secret := []byte("integration-test-hardware-secret")
	writeFakeYkchalresp(t, hex.EncodeToString(secret))

	repoDir := filepath.Join(t.TempDir(), "repo")
	password := []byte("init-password-e2e")

	// 1. init with a password (bootstrap slot).
	be, err := local.New(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	r, err := repo.Init(ctx, be, password)
	if err != nil {
		t.Fatal(err)
	}

	// 2. key add --yubikey, exercised via AddYubiKeySlot + the real exec
	// Responder (which will shell out to our fake ykchalresp on PATH).
	responder := yubikey.NewExecResponder()
	if !yubikey.DetectAvailable() {
		t.Fatal("fake ykchalresp not detected on PATH")
	}
	slotID, err := r.AddYubiKeySlot(ctx, responder, 2)
	if err != nil {
		t.Fatalf("AddYubiKeySlot: %v", err)
	}
	if err := r.Close(ctx); err != nil {
		t.Fatal(err)
	}

	// 3. Open the repository using ONLY the YubiKey (no password at all) -
	// this is the passwordless-unlock scenario end to end through the real
	// external-command path.
	be2, err := local.New(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := repo.OpenWithYubiKey(ctx, be2, responder)
	if err != nil {
		t.Fatalf("OpenWithYubiKey: %v", err)
	}
	if r2.OpenedKeyID() != slotID {
		t.Fatalf("opened key id = %s, want %s", r2.OpenedKeyID(), slotID)
	}
	if err := r2.Close(ctx); err != nil {
		t.Fatal(err)
	}

	// 4. The original password slot must still independently open the repo.
	be3, err := local.New(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	r3, err := repo.Open(ctx, be3, password)
	if err != nil {
		t.Fatalf("password Open failed after adding a yubikey slot: %v", err)
	}
	defer r3.Close(ctx)

	slots, err := r3.ListKeySlots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	types := map[repo.KeyType]int{}
	for _, s := range slots {
		types[s.Type]++
	}
	if types[repo.KeyTypePassword] != 1 || types[repo.KeyTypeYubiKey] != 1 {
		t.Fatalf("unexpected slot type counts: %v", types)
	}
}
