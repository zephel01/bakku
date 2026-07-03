package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolvePasswordEnvPrecedence(t *testing.T) {
	t.Setenv("BAKKU_PASSWORD", "from-env")
	// Even with a command set, env wins.
	pw, err := ResolvePassword(PasswordOptions{Command: "echo should-not-run"})
	if err != nil {
		t.Fatal(err)
	}
	if string(pw) != "from-env" {
		t.Fatalf("expected env password, got %q", pw)
	}
}

func TestResolvePasswordFileBeatsCommand(t *testing.T) {
	os.Unsetenv("BAKKU_PASSWORD")
	dir := t.TempDir()
	f := filepath.Join(dir, "pw")
	if err := os.WriteFile(f, []byte("from-file\nignored\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pw, err := ResolvePassword(PasswordOptions{File: f, Command: "echo from-cmd"})
	if err != nil {
		t.Fatal(err)
	}
	if string(pw) != "from-file" {
		t.Fatalf("expected file password, got %q", pw)
	}
}

func TestResolvePasswordCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test uses POSIX sh")
	}
	os.Unsetenv("BAKKU_PASSWORD")
	pw, err := ResolvePassword(PasswordOptions{Command: "printf 'cmd-secret\\nsecond-line\\n'"})
	if err != nil {
		t.Fatal(err)
	}
	if string(pw) != "cmd-secret" {
		t.Fatalf("expected first line only, got %q", pw)
	}
}

func TestResolvePasswordCommandBeatsConfigCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test uses POSIX sh")
	}
	os.Unsetenv("BAKKU_PASSWORD")
	pw, err := ResolvePassword(PasswordOptions{
		Command:       "echo flag-cmd",
		ConfigCommand: "echo config-cmd",
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(pw) != "flag-cmd" {
		t.Fatalf("expected --password-command to win, got %q", pw)
	}
}

func TestResolvePasswordConfigCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test uses POSIX sh")
	}
	os.Unsetenv("BAKKU_PASSWORD")
	pw, err := ResolvePassword(PasswordOptions{ConfigCommand: "echo config-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if string(pw) != "config-secret" {
		t.Fatalf("expected config command password, got %q", pw)
	}
}

func TestResolvePasswordCommandFailureIsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test uses POSIX sh")
	}
	os.Unsetenv("BAKKU_PASSWORD")
	_, err := ResolvePassword(PasswordOptions{Command: "exit 3"})
	if err == nil {
		t.Fatal("expected error from failing password command")
	}
}

func TestResolvePasswordKeychainFallsThrough(t *testing.T) {
	// In the sandbox there is no D-Bus secret service, so a keychain lookup
	// returns ErrUnavailable/ErrNotFound; with no terminal, ResolvePassword must
	// fall through and error on the interactive prompt (not panic or hang).
	os.Unsetenv("BAKKU_PASSWORD")
	_, err := ResolvePassword(PasswordOptions{KeychainRepo: "file:///nonexistent/repo"})
	if err == nil {
		t.Fatal("expected an error (no terminal) after keychain fall-through")
	}
}

func TestDestPasswordCommandResolution(t *testing.T) {
	c := &Config{
		PasswordCommand: "global-cmd",
		Dests: []Dest{
			{Name: "withcmd", URL: "file:///a", PasswordCommand: "dest-cmd"},
			{Name: "nocmd", URL: "file:///b"},
		},
	}
	// Per-dest command wins when set.
	if got := c.DestPasswordCommand("withcmd", "file:///a"); got != "dest-cmd" {
		t.Fatalf("expected dest-cmd, got %q", got)
	}
	// Falls back to global when the dest has no command.
	if got := c.DestPasswordCommand("nocmd", "file:///b"); got != "global-cmd" {
		t.Fatalf("expected global-cmd, got %q", got)
	}
	// Match by URL also works.
	if got := c.DestPasswordCommand("file:///a", "file:///a"); got != "dest-cmd" {
		t.Fatalf("expected dest-cmd by URL, got %q", got)
	}
	// Unknown repo -> global.
	if got := c.DestPasswordCommand("file:///z", "file:///z"); got != "global-cmd" {
		t.Fatalf("expected global-cmd for unknown, got %q", got)
	}
}
