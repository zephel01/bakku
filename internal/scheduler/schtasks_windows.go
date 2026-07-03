//go:build windows

package scheduler

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const taskNamePrefix = `Bakku\`

func taskName(jobName string) string { return taskNamePrefix + jobName }

// Install registers job as a Windows Scheduled Task via schtasks.exe.
//
// schtasks' /SC (schedule type) + /MO/D/M options do not map cleanly onto an
// arbitrary cron expression's Cartesian product the way launchd's
// StartCalendarInterval array does (schtasks only allows one schedule rule
// per /Create call). We therefore support the common, well-defined subset
// that covers the documented use case (daily/weekly-at-a-single-time jobs)
// directly, and fall back to MINUTE-granularity /SC MINUTE /MO 1 with a
// same-process cron gate for anything more exotic is explicitly out of scope
// here: instead, for expressions outside the supported subset we return a
// descriptive error naming the unsupported combination rather than silently
// installing an incorrect schedule.
func Install(job Job) error {
	if err := ValidateName(job.Name); err != nil {
		return err
	}
	if err := ValidateCron(job.CronExpr); err != nil {
		return err
	}
	f, err := parseCronFields(job.CronExpr)
	if err != nil {
		return err
	}
	if len(f.Minute) != 1 || len(f.Hour) != 1 {
		return fmt.Errorf("scheduler: windows schtasks backend requires a single fixed minute and hour in the cron expression (got %d minute value(s), %d hour value(s)); e.g. \"0 3 * * *\", \"30 2 * * 1\", not step/list expressions like \"*/15 * * * *\"", len(f.Minute), len(f.Hour))
	}
	startTime := fmt.Sprintf("%02d:%02d", f.Hour[0], f.Minute[0])

	bakkuPath := job.BakkuPath
	if bakkuPath == "" {
		bakkuPath, err = os.Executable()
		if err != nil {
			return fmt.Errorf("scheduler: resolve bakku executable path: %w", err)
		}
	}
	taskRun := bakkuPath
	if len(job.Command) > 0 {
		taskRun = quoteArg(bakkuPath) + " " + quoteArgs(job.Command)
	}

	name := taskName(job.Name)
	var args []string
	switch {
	case f.Dom == nil && f.Month == nil && f.Dow == nil:
		// Every day.
		args = []string{"/Create", "/TN", name, "/TR", taskRun, "/SC", "DAILY", "/ST", startTime, "/F"}
	case f.Dom == nil && f.Month == nil && len(f.Dow) >= 1:
		days := make([]string, len(f.Dow))
		for i, d := range f.Dow {
			days[i] = schtasksDow[d]
		}
		args = []string{"/Create", "/TN", name, "/TR", taskRun, "/SC", "WEEKLY", "/D", strings.Join(days, ","), "/ST", startTime, "/F"}
	case f.Dow == nil && f.Month == nil && len(f.Dom) >= 1:
		days := make([]string, len(f.Dom))
		for i, d := range f.Dom {
			days[i] = fmt.Sprintf("%d", d)
		}
		args = []string{"/Create", "/TN", name, "/TR", taskRun, "/SC", "MONTHLY", "/D", strings.Join(days, ","), "/ST", startTime, "/F"}
	default:
		return fmt.Errorf("scheduler: windows schtasks backend does not support combined day-of-month + day-of-week + month cron constraints; simplify the expression (e.g. use either a day-of-month or day-of-week restriction, not both, and leave month as \"*\")")
	}

	manual := "schtasks " + strings.Join(quoteSchtasksArgsForDisplay(args), " ")
	if _, err := exec.LookPath("schtasks.exe"); err != nil {
		if _, err2 := exec.LookPath("schtasks"); err2 != nil {
			return fmt.Errorf("scheduler: schtasks.exe not found in PATH; run manually:\n  %s", manual)
		}
	}
	if out, err := runCommand("schtasks.exe", args...); err != nil {
		return fmt.Errorf("scheduler: `schtasks /Create` failed (%v): %s\nrun manually:\n  %s", err, out, manual)
	}
	return nil
}

var schtasksDow = []string{"SUN", "MON", "TUE", "WED", "THU", "FRI", "SAT"}

func quoteSchtasksArgsForDisplay(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if strings.ContainsAny(a, " \t") {
			out[i] = `"` + a + `"`
		} else {
			out[i] = a
		}
	}
	return out
}

// Uninstall removes the Windows Scheduled Task for name via
// `schtasks /Delete`. A not-found task is not an error (idempotent
// uninstall).
func Uninstall(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	out, err := runCommand("schtasks.exe", "/Delete", "/TN", taskName(name), "/F")
	if err != nil {
		if strings.Contains(strings.ToUpper(out), "ERROR: THE SYSTEM CANNOT FIND THE FILE SPECIFIED") ||
			strings.Contains(strings.ToUpper(out), "CANNOT FIND") {
			return fmt.Errorf("scheduler: job %q was not installed", name)
		}
		return fmt.Errorf("scheduler: `schtasks /Delete` failed (%v): %s", err, out)
	}
	return nil
}

// StatusOne reports the installed/enabled state of a single job by name via
// `schtasks /Query`.
func StatusOne(name string) (Status, error) {
	if err := ValidateName(name); err != nil {
		return Status{}, err
	}
	st := Status{Name: name}
	out, err := runCommand("schtasks.exe", "/Query", "/TN", taskName(name), "/FO", "LIST")
	if err != nil {
		return st, nil // not installed
	}
	st.Installed = true
	st.Detail = strings.TrimSpace(out)
	st.Enabled = strings.Contains(out, "Ready") || strings.Contains(out, "Running")
	return st, nil
}

// List reports the status of every task under the "Bakku\" folder via
// `schtasks /Query`.
func List() ([]Status, error) {
	out, err := runCommand("schtasks.exe", "/Query", "/FO", "CSV", "/NH")
	if err != nil {
		return nil, fmt.Errorf("scheduler: `schtasks /Query` failed: %w", err)
	}
	var result []Status
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\",\"")
		if len(fields) == 0 {
			continue
		}
		tn := strings.Trim(fields[0], `"`)
		if !strings.HasPrefix(tn, `\`+taskNamePrefix) && !strings.HasPrefix(tn, taskNamePrefix) {
			continue
		}
		jobName := strings.TrimPrefix(strings.TrimPrefix(tn, `\`), taskNamePrefix)
		st, err := StatusOne(jobName)
		if err != nil {
			continue
		}
		result = append(result, st)
	}
	return result, nil
}
