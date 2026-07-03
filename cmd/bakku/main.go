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
// They default to "dev"/"none"/"unknown" for local `go build`/`go run` and
// `go install github.com/zephel01/bakku/cmd/bakku@latest` (which does not run
// our release ldflags).
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	root := cli.NewRootCmd(cli.BuildInfo{Version: version, Commit: commit, Date: date})
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
