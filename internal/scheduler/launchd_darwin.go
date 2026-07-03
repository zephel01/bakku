//go:build darwin

package scheduler

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const launchdLabelPrefix = "com.bakku."

func launchdLabel(jobName string) string { return launchdLabelPrefix + jobName }

// launchAgentsDir returns ~/Library/LaunchAgents, creating it if necessary.
func launchAgentsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("scheduler: resolve home directory: %w", err)
	}
	dir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("scheduler: create %s: %w", dir, err)
	}
	return dir, nil
}

func plistPath(dir, jobName string) string {
	return filepath.Join(dir, launchdLabel(jobName)+".plist")
}

// Install writes a launchd agent plist for job and loads it via
// `launchctl load -w`. If the launchctl invocation fails, the plist is left
// in place and the manual command is returned in the error.
func Install(job Job) error {
	if err := ValidateName(job.Name); err != nil {
		return err
	}
	if err := ValidateCron(job.CronExpr); err != nil {
		return err
	}
	intervals, err := ToStartCalendarInterval(job.CronExpr)
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

	dir, err := launchAgentsDir()
	if err != nil {
		return err
	}

	plist := buildPlist(launchdLabel(job.Name), bakkuPath, job.Command, intervals)
	path := plistPath(dir, job.Name)
	if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
		return fmt.Errorf("scheduler: write %s: %w", path, err)
	}

	manual := fmt.Sprintf("launchctl load -w %s", path)
	if _, err := exec.LookPath("launchctl"); err != nil {
		return fmt.Errorf("scheduler: plist written to %s, but launchctl was not found in PATH; run manually:\n  %s", path, manual)
	}
	if out, err := runCommand("launchctl", "load", "-w", path); err != nil {
		return fmt.Errorf("scheduler: plist written to %s, but `launchctl load -w` failed (%v): %s\nrun manually:\n  %s", path, err, out, manual)
	}
	return nil
}

// Uninstall unloads and removes the launchd agent plist for name. Missing
// plists are not an error (idempotent uninstall).
func Uninstall(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	dir, err := launchAgentsDir()
	if err != nil {
		return err
	}
	path := plistPath(dir, name)

	existed := false
	if _, err := os.Stat(path); err == nil {
		existed = true
	}

	if existed {
		if _, err := exec.LookPath("launchctl"); err == nil {
			_, _ = runCommand("launchctl", "unload", "-w", path)
		}
	}

	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			if !existed {
				return fmt.Errorf("scheduler: job %q was not installed", name)
			}
			return nil
		}
		return fmt.Errorf("scheduler: remove %s: %w", path, err)
	}
	return nil
}

// StatusOne reports the installed/loaded state of a single job by name.
func StatusOne(name string) (Status, error) {
	if err := ValidateName(name); err != nil {
		return Status{}, err
	}
	dir, err := launchAgentsDir()
	if err != nil {
		return Status{}, err
	}
	st := Status{Name: name}
	if _, err := os.Stat(plistPath(dir, name)); err != nil {
		return st, nil
	}
	st.Installed = true
	if _, lookErr := exec.LookPath("launchctl"); lookErr == nil {
		out, _ := runCommand("launchctl", "list", launchdLabel(name))
		if strings.Contains(out, launchdLabel(name)) {
			st.Enabled = true
			st.Detail = "loaded"
		} else {
			st.Detail = "not loaded"
		}
	}
	return st, nil
}

// List reports the status of every bakku-managed launchd agent found in
// ~/Library/LaunchAgents.
func List() ([]Status, error) {
	dir, err := launchAgentsDir()
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
		const suffix = ".plist"
		if !strings.HasPrefix(name, launchdLabelPrefix) || !strings.HasSuffix(name, suffix) {
			continue
		}
		jobName := strings.TrimSuffix(strings.TrimPrefix(name, launchdLabelPrefix), suffix)
		st, err := StatusOne(jobName)
		if err != nil {
			continue
		}
		out = append(out, st)
	}
	return out, nil
}

// buildPlist renders a launchd agent plist with one StartCalendarInterval
// dict per LaunchdCalendarInterval entry.
func buildPlist(label, bakkuPath string, command []string, intervals []LaunchdCalendarInterval) string {
	var sb strings.Builder
	sb.WriteString(xml.Header)
	sb.WriteString("<plist version=\"1.0\">\n<dict>\n")
	sb.WriteString("\t<key>Label</key>\n\t<string>" + xmlEscape(label) + "</string>\n")

	sb.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	sb.WriteString("\t\t<string>" + xmlEscape(bakkuPath) + "</string>\n")
	for _, arg := range command {
		sb.WriteString("\t\t<string>" + xmlEscape(arg) + "</string>\n")
	}
	sb.WriteString("\t</array>\n")

	sb.WriteString("\t<key>StartCalendarInterval</key>\n\t<array>\n")
	for _, iv := range intervals {
		sb.WriteString("\t\t<dict>\n")
		if iv.HasMinute {
			sb.WriteString(fmt.Sprintf("\t\t\t<key>Minute</key>\n\t\t\t<integer>%d</integer>\n", iv.Minute))
		}
		if iv.HasHour {
			sb.WriteString(fmt.Sprintf("\t\t\t<key>Hour</key>\n\t\t\t<integer>%d</integer>\n", iv.Hour))
		}
		if iv.HasDay {
			sb.WriteString(fmt.Sprintf("\t\t\t<key>Day</key>\n\t\t\t<integer>%d</integer>\n", iv.Day))
		}
		if iv.HasWeekday {
			sb.WriteString(fmt.Sprintf("\t\t\t<key>Weekday</key>\n\t\t\t<integer>%d</integer>\n", iv.Weekday))
		}
		if iv.HasMonth {
			sb.WriteString(fmt.Sprintf("\t\t\t<key>Month</key>\n\t\t\t<integer>%d</integer>\n", iv.Month))
		}
		sb.WriteString("\t\t</dict>\n")
	}
	sb.WriteString("\t</array>\n")

	sb.WriteString("\t<key>RunAtLoad</key>\n\t<false/>\n")
	sb.WriteString("\t<key>StandardOutPath</key>\n\t<string>/tmp/" + xmlEscape(label) + ".log</string>\n")
	sb.WriteString("\t<key>StandardErrorPath</key>\n\t<string>/tmp/" + xmlEscape(label) + ".err.log</string>\n")
	sb.WriteString("</dict>\n</plist>\n")
	return sb.String()
}

// xml holds the fixed plist DOCTYPE header (kept as a tiny local namespace so
// buildPlist reads cleanly above).
var xml = struct{ Header string }{
	Header: `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
`,
}

func xmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return r.Replace(s)
}
