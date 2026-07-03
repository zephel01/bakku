package scheduler

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	cron "github.com/robfig/cron/v3"
)

// cronFields is a parsed standard 5-field cron expression, each field
// expanded to its explicit sorted set of matching integer values (in the
// field's native range, e.g. Dow is 0-6 with 0=Sunday). Expansion (rather than
// working with cron's internal bitmask Schedule type) keeps the systemd
// OnCalendar= and launchd StartCalendarInterval converters simple, and lets
// each be unit-tested against plain []int expectations.
type cronFields struct {
	Minute []int // 0-59, nil means "every minute" (*)
	Hour   []int // 0-23, nil means "every hour" (*)
	Dom    []int // 1-31, nil means "every day of month" (*)
	Month  []int // 1-12, nil means "every month" (*)
	Dow    []int // 0-6 (0=Sunday), nil means "every day of week" (*)
}

// parseCronFields validates expr with robfig/cron (so error messages and
// accepted syntax match what ValidateCron/`schedule install` already enforce)
// and then independently expands each of the 5 fields into explicit value
// sets used by the OS-specific converters below.
func parseCronFields(expr string) (*cronFields, error) {
	if _, err := cron.ParseStandard(expr); err != nil {
		return nil, fmt.Errorf("scheduler: invalid cron expression %q: %w", expr, err)
	}
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("scheduler: expected 5 cron fields (m h dom mon dow), got %d in %q", len(fields), expr)
	}
	minute, err := expandField(fields[0], 0, 59, nil)
	if err != nil {
		return nil, fmt.Errorf("scheduler: minute field: %w", err)
	}
	hour, err := expandField(fields[1], 0, 23, nil)
	if err != nil {
		return nil, fmt.Errorf("scheduler: hour field: %w", err)
	}
	if fields[0] == "*" {
		minute = nil
	}
	if fields[1] == "*" {
		hour = nil
	}
	dom, err := expandField(fields[2], 1, 31, nil)
	if err != nil {
		return nil, fmt.Errorf("scheduler: day-of-month field: %w", err)
	}
	month, err := expandField(fields[3], 1, 12, monthNames)
	if err != nil {
		return nil, fmt.Errorf("scheduler: month field: %w", err)
	}
	dow, err := expandField(fields[4], 0, 6, dowNames)
	if err != nil {
		return nil, fmt.Errorf("scheduler: day-of-week field: %w", err)
	}
	// A bare "*" expands to nil (meaning "every value") so the converters can
	// distinguish "unconstrained" from "explicitly every value in range",
	// which matters for building compact OnCalendar/StartCalendarInterval
	// output (an explicit "*" prints as "*", not a huge enumerated list).
	if fields[2] == "*" {
		dom = nil
	}
	if fields[3] == "*" {
		month = nil
	}
	if fields[4] == "*" {
		dow = nil
	}
	return &cronFields{Minute: minute, Hour: hour, Dom: dom, Month: month, Dow: dow}, nil
}

var monthNames = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

var dowNames = map[string]int{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

// expandField expands a single cron field expression (e.g. "*", "*/15",
// "1-5", "0,6", "MON-FRI") into its sorted, de-duplicated set of matching
// integer values within [min,max]. names, if non-nil, maps case-insensitive
// three-letter abbreviations to values (used for month/dow).
func expandField(expr string, min, max int, names map[string]int) ([]int, error) {
	set := make(map[int]bool)
	for _, part := range strings.Split(expr, ",") {
		if err := expandRangePart(part, min, max, names, set); err != nil {
			return nil, err
		}
	}
	out := make([]int, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Ints(out)
	return out, nil
}

func expandRangePart(part string, min, max int, names map[string]int, set map[int]bool) error {
	step := 1
	rangeExpr := part
	if i := strings.IndexByte(part, '/'); i >= 0 {
		rangeExpr = part[:i]
		s, err := strconv.Atoi(part[i+1:])
		if err != nil || s <= 0 {
			return fmt.Errorf("invalid step in %q", part)
		}
		step = s
	}

	lo, hi := min, max
	switch {
	case rangeExpr == "*":
		// full range, already set above
	case strings.Contains(rangeExpr, "-"):
		bounds := strings.SplitN(rangeExpr, "-", 2)
		a, err := parseFieldValue(bounds[0], names)
		if err != nil {
			return err
		}
		b, err := parseFieldValue(bounds[1], names)
		if err != nil {
			return err
		}
		lo, hi = a, b
	default:
		v, err := parseFieldValue(rangeExpr, names)
		if err != nil {
			return err
		}
		lo, hi = v, v
	}

	if lo < min || hi > max || lo > hi {
		return fmt.Errorf("value out of range in %q (expected %d-%d)", part, min, max)
	}
	for v := lo; v <= hi; v += step {
		set[v] = true
	}
	return nil
}

func parseFieldValue(s string, names map[string]int) (int, error) {
	if names != nil {
		if v, ok := names[strings.ToLower(s)]; ok {
			return v, nil
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid value %q", s)
	}
	return v, nil
}

// ToOnCalendar converts a standard 5-field cron expression into a systemd
// timer OnCalendar= expression
// (https://www.freedesktop.org/software/systemd/man/systemd.time.html).
// Cron has no native "every N minutes/hours" concept in OnCalendar syntax
// other than an explicit list, so stepped fields (e.g. "*/15") are rendered
// as explicit comma-separated value lists, which OnCalendar supports natively
// (e.g. "*:0/15" is also valid systemd syntax, but explicit lists are simpler
// to generate correctly and remain human-readable).
func ToOnCalendar(cronExpr string) (string, error) {
	f, err := parseCronFields(cronExpr)
	if err != nil {
		return "", err
	}
	// systemd calendar format: DayOfWeek Year-Month-Day Hour:Minute:Second
	// We omit DayOfWeek unless the cron expression constrains it, and always
	// use "*" for year.
	dowPart := ""
	if f.Dow != nil {
		dowPart = joinOnCalendarDow(f.Dow) + " "
	}
	monthPart := "*"
	if f.Month != nil {
		monthPart = joinInts(f.Month)
	}
	domPart := "*"
	if f.Dom != nil {
		domPart = joinInts(f.Dom)
	}
	hourPart := "*"
	if f.Hour != nil {
		hourPart = joinInts(f.Hour)
	}
	minutePart := "*"
	if f.Minute != nil {
		minutePart = joinInts(f.Minute)
	}

	return fmt.Sprintf("%s*-%s-%s %s:%s:00", dowPart, monthPart, domPart, hourPart, minutePart), nil
}

var onCalendarDowNames = []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}

func joinOnCalendarDow(days []int) string {
	parts := make([]string, len(days))
	for i, d := range days {
		parts[i] = onCalendarDowNames[d]
	}
	return strings.Join(parts, ",")
}

func joinInts(vals []int) string {
	parts := make([]string, len(vals))
	for i, v := range vals {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, ",")
}

// LaunchdCalendarInterval is one <dict> entry under a launchd job's
// StartCalendarInterval array (or the sole dict if only one combination is
// needed). Fields with Has*=false are omitted from the rendered plist dict
// entirely, which launchd treats as "every value" for that field, matching
// cron's "*".
type LaunchdCalendarInterval struct {
	Minute     int
	Hour       int
	Day        int // day of month
	Weekday    int // 0=Sunday..6=Saturday
	Month      int // 1-12
	HasMinute  bool
	HasHour    bool
	HasDay     bool
	HasWeekday bool
	HasMonth   bool
}

// ToStartCalendarInterval converts a standard 5-field cron expression into
// the list of StartCalendarInterval dicts launchd needs to reproduce it.
// launchd's StartCalendarInterval has no list/step syntax within a single
// dict (each key is a single integer or absent-for-"every"), so a cron field
// that expands to multiple values (e.g. "*/15" minutes, or "1,15" hours)
// requires the Cartesian product of all constrained fields, one dict per
// combination. To keep the array from exploding, Dom/Month/Dow being
// unconstrained (cron "*") means that field is simply omitted from every
// dict rather than enumerated.
func ToStartCalendarInterval(cronExpr string) ([]LaunchdCalendarInterval, error) {
	f, err := parseCronFields(cronExpr)
	if err != nil {
		return nil, err
	}

	doms := f.Dom
	if doms == nil {
		doms = []int{-1}
	}
	months := f.Month
	if months == nil {
		months = []int{-1}
	}
	dows := f.Dow
	if dows == nil {
		dows = []int{-1}
	}
	hours := f.Hour
	if hours == nil {
		hours = []int{-1}
	}
	minutes := f.Minute
	if minutes == nil {
		minutes = []int{-1}
	}

	var out []LaunchdCalendarInterval
	for _, mo := range months {
		for _, d := range doms {
			for _, dw := range dows {
				for _, h := range hours {
					for _, mi := range minutes {
						out = append(out, LaunchdCalendarInterval{
							Minute:     mi,
							HasMinute:  mi != -1,
							Hour:       h,
							HasHour:    h != -1,
							Day:        d,
							HasDay:     d != -1,
							Weekday:    dw,
							HasWeekday: dw != -1,
							Month:      mo,
							HasMonth:   mo != -1,
						})
					}
				}
			}
		}
	}
	return out, nil
}
