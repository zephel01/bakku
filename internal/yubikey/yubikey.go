// Package yubikey talks to a YubiKey's HMAC-SHA1 challenge-response OTP slot
// via external command-line tools (ykchalresp from yubikey-personalization, or
// ykman from yubikey-manager). bakku never links against any YubiKey/PC-SC C
// library directly: doing so would require cgo, which breaks bakku's
// cgo-free, six-target cross-build. Shelling out keeps the binary portable.
//
// Protocol (same scheme KeePassXC uses for its YubiKey database unlock):
//
//  1. A 63-byte random challenge is generated once, when the key slot is
//     created, and stored in the (non-secret) key file.
//  2. The challenge is sent to the YubiKey's configured OTP slot (1 or 2;
//     bakku defaults to slot 2, the common "long press" slot for
//     challenge-response since slot 1 is often the factory Yubico OTP
//     credential). The YubiKey computes HMAC-SHA1(secret, challenge) with a
//     secret that never leaves the hardware, returning a 20-byte response.
//  3. The response is stretched into a key-encryption-key (KEK) via HKDF-SHA256
//     (see repo.deriveYubiKEKForSlot; slots created before v0.2.4 use a legacy
//     BLAKE3 derive-key path) and used to unwrap the repository master key
//     exactly like a password-derived KEK.
//
// The challenge is not secret: without the physical YubiKey (and its
// programmed HMAC secret) it is computationally infeasible to reproduce the
// response. Only the derived KEK material is sensitive, and that is never
// persisted.
package yubikey

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// ResponseSize is the length of a YubiKey HMAC-SHA1 challenge-response.
const ResponseSize = 20

// ChallengeSize is the challenge length bakku generates for new slots. 63
// bytes matches KeePassXC/ykpers convention: HMAC-SHA1 challenge-response
// slots accept up to 64 bytes and libykpers pads/handles 63 bytes without the
// variable-length quirks some firmware has at the full 64-byte boundary.
const ChallengeSize = 63

// TouchTimeout bounds how long bakku waits for the user to touch the YubiKey.
const TouchTimeout = 30 * time.Second

// Responder performs a single HMAC-SHA1 challenge-response exchange with a
// YubiKey OTP slot. Implementations may talk to real hardware (via an
// external command) or, in tests, return canned/deterministic responses.
type Responder interface {
	// Respond sends challenge to the given OTP slot (1 or 2) and returns the
	// 20-byte HMAC-SHA1 response. Implementations should surface a clear error
	// if no YubiKey tool is available or the operation times out.
	Respond(ctx context.Context, slot int, challenge []byte) (response [ResponseSize]byte, err error)
}

// ErrNoTool is returned when neither ykchalresp nor ykman is found on PATH.
var ErrNoTool = errors.New("yubikey: no YubiKey tool found on PATH; install ykchalresp (yubikey-personalization) or ykman (yubikey-manager)")

// lookPath is a package-level indirection so tests can stub tool discovery
// without touching the real PATH.
var lookPath = exec.LookPath

// runCommand is a package-level indirection so tests can stub command
// execution (a fake tool script) without invoking real binaries.
var runCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg != "" {
			return nil, fmt.Errorf("%s: %w: %s", name, err, msg)
		}
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return stdout.Bytes(), nil
}

// tool identifies which external command to use and how to invoke it.
type tool struct {
	// name is the command to look up on PATH (e.g. "ykchalresp", "ykman").
	name string
	// buildArgs returns the argv (excluding argv[0]) for a challenge-response
	// request on the given OTP slot with the given hex-encoded challenge.
	buildArgs func(slot int, hexChallenge string) []string
}

// candidateTools lists the supported external tools in detection priority
// order: ykchalresp (yubikey-personalization) is tried first, then ykman
// (yubikey-manager).
var candidateTools = []tool{
	{
		name: "ykchalresp",
		buildArgs: func(slot int, hexChallenge string) []string {
			return []string{fmt.Sprintf("-%d", slot), "-x", hexChallenge}
		},
	},
	{
		name: "ykman",
		buildArgs: func(slot int, hexChallenge string) []string {
			return []string{"otp", "calculate", fmt.Sprintf("%d", slot), hexChallenge}
		},
	},
}

// detectTool returns the first candidate tool found on PATH, or ErrNoTool if
// none is available.
func detectTool() (tool, error) {
	for _, t := range candidateTools {
		if _, err := lookPath(t.name); err == nil {
			return t, nil
		}
	}
	return tool{}, ErrNoTool
}

// execResponder is the production Responder: it shells out to ykchalresp or
// ykman (whichever is found first on PATH).
type execResponder struct{}

// NewExecResponder returns a Responder that talks to a real YubiKey through
// whichever supported external tool is available on PATH. Use DetectAvailable
// to check availability up front (e.g. for CLI auto-fallback decisions)
// without attempting a touch-requiring exchange.
func NewExecResponder() Responder { return execResponder{} }

// DetectAvailable reports whether a supported YubiKey tool (ykchalresp or
// ykman) is present on PATH, without invoking it.
func DetectAvailable() bool {
	_, err := detectTool()
	return err == nil
}

func (execResponder) Respond(ctx context.Context, slot int, challenge []byte) ([ResponseSize]byte, error) {
	var out [ResponseSize]byte
	t, err := detectTool()
	if err != nil {
		return out, err
	}

	fmt.Fprintln(os.Stderr, "YubiKeyにタッチしてください... (touch your YubiKey now)")

	cctx, cancel := context.WithTimeout(ctx, TouchTimeout)
	defer cancel()

	hexChallenge := hex.EncodeToString(challenge)
	raw, err := runCommand(cctx, t.name, t.buildArgs(slot, hexChallenge)...)
	if err != nil {
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			return out, fmt.Errorf("yubikey: timed out waiting for touch after %s", TouchTimeout)
		}
		return out, fmt.Errorf("yubikey: %s failed: %w", t.name, err)
	}
	resp, err := parseResponse(raw)
	if err != nil {
		return out, fmt.Errorf("yubikey: parse %s output: %w", t.name, err)
	}
	return resp, nil
}

// hexToken matches the first contiguous run of hex digits in a tool's output,
// which is where both ykchalresp and ykman place the response regardless of
// surrounding whitespace, labels, or trailing newlines.
var hexToken = regexp.MustCompile(`[0-9a-fA-F]{40,}`)

// parseResponse extracts the 20-byte HMAC-SHA1 response from a tool's raw
// stdout. Supported formats:
//
//   - ykchalresp -2 -x <hex>:       "<40 hex chars>\n"
//   - ykman otp calculate 2 <hex>:  "<40 hex chars>\n" (some versions print
//     extra informational lines before/after the token; hexToken skips those)
func parseResponse(raw []byte) ([ResponseSize]byte, error) {
	var out [ResponseSize]byte
	s := strings.TrimSpace(string(raw))
	match := hexToken.FindString(s)
	if match == "" {
		return out, fmt.Errorf("no hex response found in output %q", s)
	}
	// A response line may be longer than 40 hex chars only if concatenated
	// with adjacent text that also looks hex; enforce an exact decode of the
	// first 40 characters (20 bytes), which is the fixed HMAC-SHA1 size.
	if len(match) < ResponseSize*2 {
		return out, fmt.Errorf("response too short: got %d hex chars, want %d", len(match), ResponseSize*2)
	}
	decoded, err := hex.DecodeString(match[:ResponseSize*2])
	if err != nil {
		return out, fmt.Errorf("invalid hex response: %w", err)
	}
	copy(out[:], decoded)
	return out, nil
}
