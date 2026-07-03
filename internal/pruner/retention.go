// Package pruner implements bakku's generation-management (forget) and
// garbage-collection (prune) logic: GFS retention selection, reachability
// analysis over snapshots/trees, and pack repacking.
package pruner

import (
	"sort"
	"time"

	"github.com/zephel01/bakku/internal/repo"
)

// Policy describes a GFS (grandfather-father-son) retention policy. A zero value
// (all fields <= 0 and no KeepTags) keeps nothing, so callers should validate
// that at least one keep-* is set before applying. KeepLast/Daily/... follow
// restic semantics: keep the newest snapshot in each of the N most recent
// buckets for that period.
type Policy struct {
	KeepLast    int
	KeepDaily   int
	KeepWeekly  int
	KeepMonthly int
	KeepYearly  int
	KeepTags    []string // snapshots carrying any of these tags are always kept
}

// Empty reports whether the policy would keep nothing (no rule set).
func (p Policy) Empty() bool {
	return p.KeepLast <= 0 && p.KeepDaily <= 0 && p.KeepWeekly <= 0 &&
		p.KeepMonthly <= 0 && p.KeepYearly <= 0 && len(p.KeepTags) == 0
}

// Decision is the forget outcome for one snapshot.
type Decision struct {
	Snapshot *repo.Snapshot
	Keep     bool
	Reasons  []string // why it was kept (e.g. "last", "daily", "tag:foo")
}

// ApplyGrouped groups snapshots by (hostname + sorted paths) and applies the
// policy within each group independently, mirroring restic's default grouping.
// It returns decisions in newest-first order across all groups.
func ApplyGrouped(snaps []*repo.Snapshot, p Policy) []Decision {
	groups := groupSnapshots(snaps)
	var all []Decision
	// Deterministic group order by key.
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		all = append(all, Apply(groups[k], p)...)
	}
	// Sort combined newest-first for stable presentation.
	sort.SliceStable(all, func(i, j int) bool {
		return all[i].Snapshot.Time.After(all[j].Snapshot.Time)
	})
	return all
}

// groupSnapshots keys snapshots by hostname + their path set.
func groupSnapshots(snaps []*repo.Snapshot) map[string][]*repo.Snapshot {
	m := make(map[string][]*repo.Snapshot)
	for _, s := range snaps {
		paths := append([]string(nil), s.Paths...)
		sort.Strings(paths)
		key := s.Hostname + "\x00"
		for _, p := range paths {
			key += p + "\x00"
		}
		m[key] = append(m[key], s)
	}
	return m
}

// Apply runs the GFS selection over a single group of snapshots and returns a
// keep/forget decision for each. The algorithm walks snapshots newest-first and,
// for each period, keeps the first snapshot seen in a new time-bucket until the
// KeepN budget for that period is exhausted (restic-compatible).
func Apply(snaps []*repo.Snapshot, p Policy) []Decision {
	ordered := append([]*repo.Snapshot(nil), snaps...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].Time.After(ordered[j].Time)
	})

	tagSet := make(map[string]struct{}, len(p.KeepTags))
	for _, t := range p.KeepTags {
		tagSet[t] = struct{}{}
	}

	decisions := make([]Decision, len(ordered))

	// Per-period bucket bookkeeping: last bucket-id kept, and remaining budget.
	type bucketState struct {
		budget   int
		lastSeen string
		haveLast bool
	}
	last := bucketState{budget: p.KeepLast}
	daily := bucketState{budget: p.KeepDaily}
	weekly := bucketState{budget: p.KeepWeekly}
	monthly := bucketState{budget: p.KeepMonthly}
	yearly := bucketState{budget: p.KeepYearly}

	// consider returns true (and marks the reason) if this snapshot opens a new
	// bucket for the period and there is remaining budget.
	consider := func(st *bucketState, bucket string) bool {
		if st.budget <= 0 {
			return false
		}
		if st.haveLast && st.lastSeen == bucket {
			return false // already kept one in this bucket
		}
		st.lastSeen = bucket
		st.haveLast = true
		st.budget--
		return true
	}

	for i, s := range ordered {
		d := Decision{Snapshot: s}

		// Tag-based retention (always keep).
		for _, t := range s.Tags {
			if _, ok := tagSet[t]; ok {
				d.Keep = true
				d.Reasons = append(d.Reasons, "tag:"+t)
				break
			}
		}

		// keep-last is a simple count of the N newest, independent of time.
		if last.budget > 0 {
			last.budget--
			d.Keep = true
			d.Reasons = append(d.Reasons, "last")
		}

		t := s.Time
		if consider(&daily, bucketDay(t)) {
			d.Keep = true
			d.Reasons = append(d.Reasons, "daily")
		}
		if consider(&weekly, bucketWeek(t)) {
			d.Keep = true
			d.Reasons = append(d.Reasons, "weekly")
		}
		if consider(&monthly, bucketMonth(t)) {
			d.Keep = true
			d.Reasons = append(d.Reasons, "monthly")
		}
		if consider(&yearly, bucketYear(t)) {
			d.Keep = true
			d.Reasons = append(d.Reasons, "yearly")
		}

		decisions[i] = d
	}
	return decisions
}

// Bucket id helpers. Times are compared in their own location so that "daily"
// respects the wall-clock day each snapshot was taken.
func bucketDay(t time.Time) string {
	y, m, d := t.Date()
	return isoKey(y, int(m), d)
}

func bucketMonth(t time.Time) string {
	y, m, _ := t.Date()
	return isoKey(y, int(m), 0)
}

func bucketYear(t time.Time) string {
	y, _, _ := t.Date()
	return isoKey(y, 0, 0)
}

// bucketWeek uses ISO-8601 year-week so a Sunday->Monday crossing lands in the
// correct week even across year boundaries.
func bucketWeek(t time.Time) string {
	y, w := t.ISOWeek()
	return isoKey(y, w, -1)
}

func isoKey(a, b, c int) string {
	// Compact, collision-free key across the three fields.
	return itoa(a) + "-" + itoa(b) + "-" + itoa(c)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
