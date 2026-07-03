// Package scheduler installs, removes, and reports on OS-native recurring
// jobs ("bakku schedule install/uninstall/status") that invoke a bakku
// subcommand (typically `backup`) on a cron-style schedule:
//
//   - Linux:   a systemd --user service + timer under ~/.config/systemd/user/.
//   - macOS:   a launchd agent plist under ~/Library/LaunchAgents/.
//   - Windows: a Scheduled Task created via schtasks.exe.
//
// All three backends are driven by the same Job description and cron
// expression, so the CLI layer never branches on GOOS beyond calling
// Install/Uninstall/Status from this package.
package scheduler

import (
	"fmt"
	"os/exec"
	"strings"

	cron "github.com/robfig/cron/v3"
)

// Job describes a single scheduled invocation of bakku.
type Job struct {
	// Name uniquely identifies the job; it is used to derive unit/plist/task
	// names (e.g. "com.bakku.<name>", "bakku-<name>.service") and must be safe
	// to embed in a filename (letters, digits, dash, underscore).
	Name string
	// CronExpr is a standard 5-field cron expression ("m h dom mon dow"),
	// e.g. "0 3 * * *" for daily at 03:00.
	CronExpr string
	// Command is the bakku subcommand and arguments to run, e.g.
	// []string{"backup", "/home/alice/Documents", "--repo", "laptop"}. It does
	// NOT include the bakku binary itself; installers resolve that via
	// BakkuPath (or os.Executable if empty).
	Command []string
	// BakkuPath is the absolute path to the bakku binary to invoke. If empty,
	// installers resolve it via os.Executable() at install time.
	BakkuPath string
}

// Status describes the installed state of a job as reported by the OS
// scheduler (systemd/launchd/schtasks), independent of whether bakku's own
// records exist.
type Status struct {
	Name      string
	Installed bool
	Enabled   bool   // service/task is enabled to run (not just present)
	Detail    string // OS-scheduler-reported detail (state, next run, etc.), best-effort
}

// ErrNotSupported is returned by platform installers that have no
// implementation on the current OS build.
var ErrNotSupported = fmt.Errorf("scheduler: not supported on this platform")

// ValidateCron parses a standard 5-field cron expression, returning a
// descriptive error if it is invalid. It is used both to fail fast in
// `schedule install` and by the cron-to-OnCalendar/StartCalendarInterval
// converters, which need a parsed schedule to enumerate trigger times.
func ValidateCron(expr string) error {
	_, err := cron.ParseStandard(expr)
	if err != nil {
		return fmt.Errorf("scheduler: invalid cron expression %q: %w", expr, err)
	}
	return nil
}

// ValidateName reports whether name is safe to use in unit/plist/task
// filenames and identifiers.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("scheduler: job name must not be empty")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			continue
		default:
			return fmt.Errorf("scheduler: job name %q contains invalid character %q (allowed: letters, digits, - and _)", name, r)
		}
	}
	return nil
}

// quoteArg shell-quotes a single argument for embedding in a generated unit
// file's ExecStart= line (systemd) or shell wrapper, using single quotes and
// escaping any embedded single quote the POSIX-shell way: close, escaped
// quote, reopen.
func quoteArg(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, " \t\n'\"$&|;<>()`\\*?[]{}~#!") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// quoteArgs shell-quotes and joins args with spaces.
func quoteArgs(args []string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = quoteArg(a)
	}
	return strings.Join(parts, " ")
}

// runCommand runs name with args, returning combined output and error;
// installers use this to invoke systemctl/launchctl/schtasks and surface
// failures with the command's own diagnostic output.
func runCommand(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
