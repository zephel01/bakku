package cli

import (
	"fmt"
	"os"

	"github.com/zephel01/bakku/internal/notify"
)

// newNotifier loads the [notify] config section and returns a ready-to-use
// Notifier plus whether notifications are actually enabled for this run
// (config has a webhook_url configured AND the caller did not pass
// --no-notify). Callers should treat a disabled notifier as a complete
// no-op: no config is read, no network call is attempted.
//
// Config load failures are non-fatal: they are reported as a warning on
// stderr and notifications are simply disabled for the run, since a broken
// config file should never prevent a backup/prune from completing.
func newNotifier(noNotify bool) (*notify.Notifier, bool) {
	if noNotify {
		return nil, false
	}
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: notify: failed to load config: %v\n", err)
		return nil, false
	}
	if !cfg.Notify.Enabled() {
		return nil, false
	}
	return notify.New(cfg.Notify), true
}
