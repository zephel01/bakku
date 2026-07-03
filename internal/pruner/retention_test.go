package pruner

import (
	"testing"
	"time"

	"github.com/zephel01/bakku/internal/repo"
)

func snap(id string, t time.Time, tags ...string) *repo.Snapshot {
	return &repo.Snapshot{
		ID:       id,
		Time:     t,
		Hostname: "host",
		Paths:    []string{"/data"},
		Tags:     tags,
	}
}

func keptIDs(decs []Decision) map[string]bool {
	m := make(map[string]bool)
	for _, d := range decs {
		if d.Keep {
			m[d.Snapshot.ID] = true
		}
	}
	return m
}

func TestKeepLast(t *testing.T) {
	base := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	snaps := []*repo.Snapshot{
		snap("a", base),
		snap("b", base.Add(-24*time.Hour)),
		snap("c", base.Add(-48*time.Hour)),
		snap("d", base.Add(-72*time.Hour)),
	}
	decs := Apply(snaps, Policy{KeepLast: 2})
	kept := keptIDs(decs)
	if len(kept) != 2 || !kept["a"] || !kept["b"] {
		t.Fatalf("keep-last 2 kept %v, want {a,b}", kept)
	}
}

// Same-day multiple snapshots: keep-daily 1 must keep only the newest of the day.
func TestKeepDailySameDay(t *testing.T) {
	day := time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)
	snaps := []*repo.Snapshot{
		snap("morning", day.Add(8*time.Hour)),
		snap("noon", day.Add(12*time.Hour)),
		snap("evening", day.Add(20*time.Hour)),
		// previous day
		snap("prev", day.Add(-4*time.Hour)),
	}
	decs := Apply(snaps, Policy{KeepDaily: 2})
	kept := keptIDs(decs)
	// Newest of 2026-03-05 is "evening"; newest of 2026-03-04 is "prev".
	if !kept["evening"] || !kept["prev"] {
		t.Fatalf("daily kept %v, want evening+prev", kept)
	}
	if kept["morning"] || kept["noon"] {
		t.Fatalf("daily should not keep earlier same-day snapshots: %v", kept)
	}
	if len(kept) != 2 {
		t.Fatalf("daily kept %d, want 2", len(kept))
	}
}

// Week boundary: a Sunday and the following Monday fall in different ISO weeks.
func TestKeepWeeklyBoundary(t *testing.T) {
	// 2026-01-04 is a Sunday (ISO week 1), 2026-01-05 is a Monday (ISO week 2).
	sun := time.Date(2026, 1, 4, 10, 0, 0, 0, time.UTC)
	mon := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	if _, w1 := sun.ISOWeek(); w1 != 1 {
		t.Fatalf("sanity: sunday ISO week = %d, want 1", w1)
	}
	if _, w2 := mon.ISOWeek(); w2 != 2 {
		t.Fatalf("sanity: monday ISO week = %d, want 2", w2)
	}
	snaps := []*repo.Snapshot{snap("mon", mon), snap("sun", sun)}
	decs := Apply(snaps, Policy{KeepWeekly: 2})
	kept := keptIDs(decs)
	if !kept["mon"] || !kept["sun"] {
		t.Fatalf("weekly across boundary kept %v, want both", kept)
	}

	// With keep-weekly 1, only the newest week (mon) survives.
	decs2 := Apply(snaps, Policy{KeepWeekly: 1})
	kept2 := keptIDs(decs2)
	if !kept2["mon"] || kept2["sun"] || len(kept2) != 1 {
		t.Fatalf("weekly 1 kept %v, want only mon", kept2)
	}
}

func TestKeepTag(t *testing.T) {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	snaps := []*repo.Snapshot{
		snap("keepme", base.Add(-100*24*time.Hour), "important"),
		snap("recent", base),
	}
	decs := Apply(snaps, Policy{KeepLast: 1, KeepTags: []string{"important"}})
	kept := keptIDs(decs)
	if !kept["keepme"] || !kept["recent"] {
		t.Fatalf("tag+last kept %v, want both", kept)
	}
}

func TestEmptyPolicy(t *testing.T) {
	if !(Policy{}).Empty() {
		t.Fatal("zero policy should be Empty")
	}
	if (Policy{KeepLast: 1}).Empty() {
		t.Fatal("keep-last policy should not be Empty")
	}
}

// Grouping: two hosts must be evaluated independently.
func TestApplyGroupedByHost(t *testing.T) {
	base := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	mk := func(id, host string, t time.Time) *repo.Snapshot {
		return &repo.Snapshot{ID: id, Time: t, Hostname: host, Paths: []string{"/x"}}
	}
	snaps := []*repo.Snapshot{
		mk("h1a", "h1", base),
		mk("h1b", "h1", base.Add(-24*time.Hour)),
		mk("h2a", "h2", base),
		mk("h2b", "h2", base.Add(-24*time.Hour)),
	}
	decs := ApplyGrouped(snaps, Policy{KeepLast: 1})
	kept := keptIDs(decs)
	// keep-last 1 applies per group, so each host keeps its newest.
	if !kept["h1a"] || !kept["h2a"] || len(kept) != 2 {
		t.Fatalf("grouped keep-last kept %v, want {h1a,h2a}", kept)
	}
}

// Combined GFS: yearly+monthly+daily should retain the right representatives.
func TestCombinedPolicy(t *testing.T) {
	var snaps []*repo.Snapshot
	// One snapshot per day for 40 days ending 2026-02-09.
	end := time.Date(2026, 2, 9, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 40; i++ {
		d := end.Add(time.Duration(-i) * 24 * time.Hour)
		snaps = append(snaps, snap(d.Format("2006-01-02"), d))
	}
	decs := Apply(snaps, Policy{KeepLast: 3, KeepDaily: 7, KeepMonthly: 2})
	kept := keptIDs(decs)
	// keep-last 3 => 3 newest days. keep-daily 7 => 7 most recent distinct days
	// (overlaps with last). keep-monthly 2 => newest of Feb + newest of Jan.
	if !kept["2026-02-09"] {
		t.Fatalf("combined must keep newest; kept=%v", kept)
	}
	// Jan representative: newest January snapshot is 2026-01-31.
	if !kept["2026-01-31"] {
		t.Fatalf("combined must keep newest January snapshot as monthly; kept=%v", kept)
	}
	// It must not keep everything.
	if len(kept) >= len(snaps) {
		t.Fatalf("combined kept all %d snapshots (no forgetting happened)", len(kept))
	}
}
