// Command bakku is a cross-platform, encrypted, deduplicating backup CLI.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/zephel01/bakku/internal/cli"
)

// version, commit, and date are set at build time via:
//
//	go build -ldflags "-X main.version=v1.2.3 -X main.commit=<sha> -X main.date=<rfc3339>"
//
// They default to "dev"/"none"/"unknown"; cli.ResolveBuildInfo then fills any
// still-default field from the Go toolchain's embedded build metadata, so
// `go install .../cmd/bakku@vX.Y.Z` reports vX.Y.Z and a plain `go build`
// inside a git checkout reports the commit, without needing our release
// ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	root := cli.NewRootCmd(cli.ResolveBuildInfo(cli.BuildInfo{Version: version, Commit: commit, Date: date}))
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
