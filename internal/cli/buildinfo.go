package cli

import (
	"runtime/debug"
)

// ldflags defaults; anything else means the value was injected at build time.
const (
	devVersion  = "dev"
	noCommit    = "none"
	unknownDate = "unknown"
)

// ResolveBuildInfo fills BuildInfo fields that were not injected via -ldflags
// (i.e. still at their "dev"/"none"/"unknown" defaults) from the module and
// VCS metadata the Go toolchain embeds into every binary (Go 1.18+):
//
//   - `go install github.com/zephel01/bakku/cmd/bakku@v0.2.0` embeds the
//     module version "v0.2.0" (no VCS info is available for module builds).
//   - A plain `go build` inside a git checkout embeds vcs.revision/vcs.time
//     (unless -buildvcs=false), so the commit and date can still be reported
//     even though the module version is "(devel)".
//
// ldflags-injected values always win; this is only a fallback.
func ResolveBuildInfo(info BuildInfo) BuildInfo {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}
	return mergeBuildInfo(info, bi)
}

// mergeBuildInfo is the testable core of ResolveBuildInfo.
func mergeBuildInfo(info BuildInfo, bi *debug.BuildInfo) BuildInfo {
	if info.Version == devVersion {
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			info.Version = v
		}
	}
	var revision, vcsTime string
	modified := false
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.time":
			vcsTime = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if info.Commit == noCommit && revision != "" {
		if len(revision) > 12 {
			revision = revision[:12]
		}
		if modified {
			revision += "-dirty"
		}
		info.Commit = revision
	}
	if info.Date == unknownDate && vcsTime != "" {
		info.Date = vcsTime
	}
	return info
}
