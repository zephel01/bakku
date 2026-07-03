//go:build linux

package scheduler

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// systemdUserDir returns ~/.config/systemd/user, creating it if necessary.
func systemdUserDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("scheduler: resolve home directory: %w", err)
	}
	dir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("scheduler: create %s: %w", dir, err)
	}
	return dir, nil
}

func unitBaseName(jobName string) string { return "bakku-" + jobName }

func servicePath(dir, jobName string) string {
	return filepath.Join(dir, unitBaseName(jobName)+".service")
}
func timerPath(dir, jobName string) string { return filepath.Join(dir, unitBaseName(jobName)+".timer") }

// Install writes a systemd --user service+timer pair for job and enables the
// timer via `systemctl --user enable --now`. If the systemctl invocation
// fails (e.g. no user session / no lingering systemd instance available,
// common in containers or SSH sessions without loginctl linger enabled), the
// unit files are left in place and the manual commands to run are returned
// in the error so the caller can print them.
func Install(job Job) error {
	if err := ValidateName(job.Name); err != nil {
		return err
	}
	if err := ValidateCron(job.CronExpr); err != nil {
		return err
	}
	onCalendar, err := ToOnCalendar(job.CronExpr)
	if err != nil {
		return err
	}
	bakkuPath := job.BakkuPath
	if bakkuPath == "" {
		bakkuPath, err = os.Executable()
		if err != nil {
			return fmt.Errorf("scheduler: resolve bakku executable path: %w", err)
		}
	}

	dir, err := systemdUserDir()
	if err != nil {
		return err
	}

	execStart := bakkuPath
	if len(job.Command) > 0 {
		execStart = quoteArg(bakkuPath) + " " + quoteArgs(job.Command)
	}

	svc := fmt.Sprintf(`[Unit]
Description=bakku scheduled job %q

[Service]
Type=oneshot
ExecStart=%s
`, job.Name, execStart)

	timer := fmt.Sprintf(`[Unit]
Description=bakku scheduled job %q timer

[Timer]
OnCalendar=%s
Persistent=true

[Install]
WantedBy=timers.target
`, job.Name, onCalendar)

	svcPath := servicePath(dir, job.Name)
	tmrPath := timerPath(dir, job.Name)
	if err := os.WriteFile(svcPath, []byte(svc), 0o644); err != nil {
		return fmt.Errorf("scheduler: write %s: %w", svcPath, err)
	}
	if err := os.WriteFile(tmrPath, []byte(timer), 0o644); err != nil {
		return fmt.Errorf("scheduler: write %s: %w", tmrPath, err)
	}

	manual := fmt.Sprintf(
		"systemctl --user daemon-reload && systemctl --user enable --now %s.timer",
		unitBaseName(job.Name),
	)

	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("scheduler: unit files written to %s and %s, but systemctl was not found in PATH; run manually:\n  %s", svcPath, tmrPath, manual)
	}

	if out, err := runCommand("systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("scheduler: unit files written to %s and %s, but `systemctl --user daemon-reload` failed (%v): %s\nrun manually:\n  %s", svcPath, tmrPath, err, out, manual)
	}
	if out, err := runCommand("systemctl", "--user", "enable", "--now", unitBaseName(job.Name)+".timer"); err != nil {
		return fmt.Errorf("scheduler: unit files written to %s and %s, but `systemctl --user enable --now` failed (%v): %s\nrun manually:\n  %s", svcPath, tmrPath, err, out, manual)
	}
	return nil
}

// Uninstall disables and removes the systemd --user unit files for job.Name.
// Missing units are not an error (idempotent uninstall).
func Uninstall(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	dir, err := systemdUserDir()
	if err != nil {
		return err
	}
	timerUnit := unitBaseName(name) + ".timer"

	var disableErr error
	if _, err := exec.LookPath("systemctl"); err == nil {
		_, disableErr = runCommand("systemctl", "--user", "disable", "--now", timerUnit)
		// A "unit not loaded" failure is expected/harmless if it was never
		// installed via systemctl (e.g. files written but enable failed
		// earlier); only surface unexpected failures below via the manual
		// hint, never abort file removal.
	}

	svcPath := servicePath(dir, name)
	tmrPath := timerPath(dir, name)
	removed := false
	for _, p := range []string{svcPath, tmrPath} {
		if err := os.Remove(p); err == nil {
			removed = true
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("scheduler: remove %s: %w", p, err)
		}
	}
	if !removed && disableErr != nil {
		return fmt.Errorf("scheduler: job %q was not installed", name)
	}
	if _, err := exec.LookPath("systemctl"); err == nil {
		_, _ = runCommand("systemctl", "--user", "daemon-reload")
	}
	return nil
}

// StatusOne reports the installed/enabled state of a single job by name.
func StatusOne(name string) (Status, error) {
	if err := ValidateName(name); err != nil {
		return Status{}, err
	}
	dir, err := systemdUserDir()
	if err != nil {
		return Status{}, err
	}
	timerUnit := unitBaseName(name) + ".timer"
	st := Status{Name: name}
	if _, err := os.Stat(timerPath(dir, name)); err == nil {
		st.Installed = true
	} else {
		return st, nil
	}
	if _, lookErr := exec.LookPath("systemctl"); lookErr == nil {
		out, _ := runCommand("systemctl", "--user", "is-enabled", timerUnit)
		st.Detail = trimNL(out)
		st.Enabled = st.Detail == "enabled"
		activeOut, _ := runCommand("systemctl", "--user", "is-active", timerUnit)
		st.Detail += " / " + trimNL(activeOut)
	}
	return st, nil
}

// List reports the status of every bakku-managed timer unit found in the
// systemd --user directory.
func List() ([]Status, error) {
	dir, err := systemdUserDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Status
	for _, e := range entries {
		name := e.Name()
		const prefix, suffix = "bakku-", ".timer"
		if len(name) <= len(prefix)+len(suffix) || name[:len(prefix)] != prefix || name[len(name)-len(suffix):] != suffix {
			continue
		}
		jobName := name[len(prefix) : len(name)-len(suffix)]
		st, err := StatusOne(jobName)
		if err != nil {
			continue
		}
		out = append(out, st)
	}
	return out, nil
}

func trimNL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
