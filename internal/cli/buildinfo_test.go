package cli

import (
	"runtime/debug"
	"testing"
)

func bi(mainVersion string, settings map[string]string) *debug.BuildInfo {
	out := &debug.BuildInfo{}
	out.Main.Version = mainVersion
	for k, v := range settings {
		out.Settings = append(out.Settings, debug.BuildSetting{Key: k, Value: v})
	}
	return out
}

func TestMergeBuildInfoModuleInstall(t *testing.T) {
	// `go install module@v0.2.0`: module version present, no VCS metadata.
	got := mergeBuildInfo(BuildInfo{Version: "dev", Commit: "none", Date: "unknown"},
		bi("v0.2.0", nil))
	if got.Version != "v0.2.0" || got.Commit != "none" || got.Date != "unknown" {
		t.Fatalf("unexpected: %+v", got)
	}
	if s := got.String(); s != "bakku v0.2.0" {
		t.Fatalf("String() = %q, want %q", s, "bakku v0.2.0")
	}
}

func TestMergeBuildInfoLocalGitBuild(t *testing.T) {
	// plain `go build` in a git checkout: "(devel)" + VCS settings.
	got := mergeBuildInfo(BuildInfo{Version: "dev", Commit: "none", Date: "unknown"},
		bi("(devel)", map[string]string{
			"vcs.revision": "0123456789abcdef0123456789abcdef01234567",
			"vcs.time":     "2026-07-03T12:00:00Z",
			"vcs.modified": "false",
		}))
	if got.Version != "dev" {
		t.Fatalf("version = %q, want dev", got.Version)
	}
	if got.Commit != "0123456789ab" {
		t.Fatalf("commit = %q, want truncated 12-char revision", got.Commit)
	}
	if got.Date != "2026-07-03T12:00:00Z" {
		t.Fatalf("date = %q", got.Date)
	}
}

func TestMergeBuildInfoDirtyWorktree(t *testing.T) {
	got := mergeBuildInfo(BuildInfo{Version: "dev", Commit: "none", Date: "unknown"},
		bi("(devel)", map[string]string{
			"vcs.revision": "0123456789abcdef0123456789abcdef01234567",
			"vcs.modified": "true",
		}))
	if got.Commit != "0123456789ab-dirty" {
		t.Fatalf("commit = %q, want -dirty suffix", got.Commit)
	}
}

func TestMergeBuildInfoLdflagsWin(t *testing.T) {
	// ldflags-injected values must never be overridden.
	in := BuildInfo{Version: "v9.9.9", Commit: "relcommit", Date: "reldate"}
	got := mergeBuildInfo(in, bi("v0.2.0", map[string]string{
		"vcs.revision": "ffffffffffff",
		"vcs.time":     "1999-01-01T00:00:00Z",
	}))
	if got != in {
		t.Fatalf("ldflags values overridden: %+v", got)
	}
}

func TestBuildInfoStringOmitsUnknowns(t *testing.T) {
	cases := map[BuildInfo]string{
		{Version: "dev", Commit: "none", Date: "unknown"}:      "bakku dev",
		{Version: "v0.2.0", Commit: "abc123", Date: "unknown"}: "bakku v0.2.0 (commit abc123)",
		{Version: "v0.2.0", Commit: "none", Date: "d"}:         "bakku v0.2.0 (built d)",
		{Version: "v0.2.0", Commit: "abc123", Date: "d"}:       "bakku v0.2.0 (commit abc123, built d)",
	}
	for in, want := range cases {
		if got := in.String(); got != want {
			t.Errorf("String(%+v) = %q, want %q", in, got, want)
		}
	}
}
