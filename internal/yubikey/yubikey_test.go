package yubikey

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParseResponsePlainHex(t *testing.T) {
	// ykchalresp's typical output: a bare 40-char hex string plus trailing
	// newline.
	want := "0102030405060708090a0b0c0d0e0f1011121314"
	got, err := parseResponse([]byte(want + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if hexOf(got) != want {
		t.Fatalf("got %s, want %s", hexOf(got), want)
	}
}

func TestParseResponseNoTrailingNewline(t *testing.T) {
	want := "aabbccddeeff00112233445566778899aabbccdd"[:40]
	got, err := parseResponse([]byte(want))
	if err != nil {
		t.Fatal(err)
	}
	if hexOf(got) != want {
		t.Fatalf("got %s, want %s", hexOf(got), want)
	}
}

func TestParseResponseYkmanStyle(t *testing.T) {
	// Some ykman versions/wrappers print a label or extra whitespace around the
	// response; parseResponse must still find the hex token.
	want := "00112233445566778899aabbccddeeff0011223f"[:40]
	raw := "  " + want + "  \n"
	got, err := parseResponse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if hexOf(got) != want {
		t.Fatalf("got %s, want %s", hexOf(got), want)
	}
}

func TestParseResponseWithSurroundingText(t *testing.T) {
	want := "1122334455667788990011223344556677889900"[:40]
	raw := "Calculated: " + want + " (done)\n"
	got, err := parseResponse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if hexOf(got) != want {
		t.Fatalf("got %s, want %s", hexOf(got), want)
	}
}

func TestParseResponseEmptyFails(t *testing.T) {
	if _, err := parseResponse([]byte("")); err == nil {
		t.Fatal("expected error for empty output")
	}
}

func TestParseResponseTooShortFails(t *testing.T) {
	if _, err := parseResponse([]byte("abcd")); err == nil {
		t.Fatal("expected error for too-short output")
	}
}

func TestParseResponseGarbageFails(t *testing.T) {
	if _, err := parseResponse([]byte("no hex here at all!!\n")); err == nil {
		t.Fatal("expected error for non-hex output")
	}
}

func TestMockResponderDeterministic(t *testing.T) {
	m := &MockResponder{Secret: []byte("device-secret")}
	challenge := bytes.Repeat([]byte{0x42}, ChallengeSize)
	a, err := m.Respond(context.Background(), 2, challenge)
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Respond(context.Background(), 2, challenge)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("mock responder is not deterministic for the same challenge")
	}
	if m.Calls != 2 {
		t.Fatalf("Calls = %d, want 2", m.Calls)
	}
}

func TestMockResponderDifferentChallengesDifferentResponses(t *testing.T) {
	m := &MockResponder{Secret: []byte("device-secret")}
	c1 := bytes.Repeat([]byte{0x01}, ChallengeSize)
	c2 := bytes.Repeat([]byte{0x02}, ChallengeSize)
	r1, _ := m.Respond(context.Background(), 2, c1)
	r2, _ := m.Respond(context.Background(), 2, c2)
	if r1 == r2 {
		t.Fatal("distinct challenges produced identical responses")
	}
}

func TestMockResponderDifferentSecretsDifferentResponses(t *testing.T) {
	challenge := bytes.Repeat([]byte{0x07}, ChallengeSize)
	m1 := &MockResponder{Secret: []byte("secret-a")}
	m2 := &MockResponder{Secret: []byte("secret-b")}
	r1, _ := m1.Respond(context.Background(), 2, challenge)
	r2, _ := m2.Respond(context.Background(), 2, challenge)
	if r1 == r2 {
		t.Fatal("distinct simulated devices produced identical responses")
	}
}

func TestMockResponderSlotMismatch(t *testing.T) {
	m := &MockResponder{Secret: []byte("s"), Slot: 2}
	_, err := m.Respond(context.Background(), 1, []byte("x"))
	if !errors.Is(err, ErrSlotMismatch) {
		t.Fatalf("expected ErrSlotMismatch, got %v", err)
	}
}

func TestDetectToolNoneAvailable(t *testing.T) {
	origLookup := lookPath
	defer func() { lookPath = origLookup }()
	lookPath = func(string) (string, error) { return "", errors.New("not found") }

	if DetectAvailable() {
		t.Fatal("expected DetectAvailable() == false when no tool is on PATH")
	}
	if _, err := detectTool(); !errors.Is(err, ErrNoTool) {
		t.Fatalf("expected ErrNoTool, got %v", err)
	}
}

func TestDetectToolPrefersYkchalresp(t *testing.T) {
	origLookup := lookPath
	defer func() { lookPath = origLookup }()
	lookPath = func(name string) (string, error) {
		return "/usr/bin/" + name, nil // both "found"
	}
	tl, err := detectTool()
	if err != nil {
		t.Fatal(err)
	}
	if tl.name != "ykchalresp" {
		t.Fatalf("expected ykchalresp to be preferred, got %s", tl.name)
	}
}

func TestDetectToolFallsBackToYkman(t *testing.T) {
	origLookup := lookPath
	defer func() { lookPath = origLookup }()
	lookPath = func(name string) (string, error) {
		if name == "ykman" {
			return "/usr/bin/ykman", nil
		}
		return "", errors.New("not found")
	}
	tl, err := detectTool()
	if err != nil {
		t.Fatal(err)
	}
	if tl.name != "ykman" {
		t.Fatalf("expected ykman fallback, got %s", tl.name)
	}
}

func TestExecResponderUsesRunCommandAndParses(t *testing.T) {
	origLookup, origRun := lookPath, runCommand
	defer func() { lookPath, runCommand = origLookup, origRun }()

	lookPath = func(name string) (string, error) {
		if name == "ykchalresp" {
			return "/usr/bin/ykchalresp", nil
		}
		return "", errors.New("not found")
	}
	var gotArgs []string
	want := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	runCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotArgs = args
		return []byte(want + "\n"), nil
	}

	r := NewExecResponder()
	challenge := bytes.Repeat([]byte{0xAB}, ChallengeSize)
	resp, err := r.Respond(context.Background(), 2, challenge)
	if err != nil {
		t.Fatal(err)
	}
	if hexOf(resp) != want {
		t.Fatalf("got %s, want %s", hexOf(resp), want)
	}
	if len(gotArgs) < 2 || gotArgs[0] != "-2" {
		t.Fatalf("unexpected args passed to ykchalresp: %v", gotArgs)
	}
}

func TestExecResponderNoToolFails(t *testing.T) {
	origLookup := lookPath
	defer func() { lookPath = origLookup }()
	lookPath = func(string) (string, error) { return "", errors.New("not found") }

	r := NewExecResponder()
	_, err := r.Respond(context.Background(), 2, bytes.Repeat([]byte{1}, ChallengeSize))
	if !errors.Is(err, ErrNoTool) {
		t.Fatalf("expected ErrNoTool, got %v", err)
	}
}

func hexOf(b [ResponseSize]byte) string {
	var sb strings.Builder
	const hexDigits = "0123456789abcdef"
	for _, c := range b {
		sb.WriteByte(hexDigits[c>>4])
		sb.WriteByte(hexDigits[c&0xf])
	}
	return sb.String()
}
