// Package notify sends best-effort webhook notifications when a backup or
// prune job finishes. Delivery failures never fail the calling command: they
// are reported to the caller as a returned error string list / logged
// warning, never propagated as a hard error.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Format selects how the webhook payload is shaped.
type Format string

const (
	// FormatJSON posts the raw Event as a JSON object (default).
	FormatJSON Format = "json"
	// FormatSlack posts a Slack-compatible {"text": "..."} payload.
	FormatSlack Format = "slack"
	// FormatDiscord posts a Discord-compatible {"content": "..."} payload.
	FormatDiscord Format = "discord"
)

// Config holds the [notify] section of config.toml.
type Config struct {
	WebhookURL string `toml:"webhook_url"`
	OnSuccess  bool   `toml:"on_success"`
	OnFailure  bool   `toml:"on_failure"`
	// Format is one of "json" (default), "slack", "discord". If empty, it is
	// inferred from the URL (hooks.slack.com -> slack, discord.com/discordapp.com
	// -> discord), else json.
	Format string `toml:"format"`
	// TimeoutSeconds bounds the webhook HTTP request; 0 uses a 10s default.
	TimeoutSeconds int `toml:"timeout_seconds"`
}

// Enabled reports whether notification is configured at all.
func (c Config) Enabled() bool { return c.WebhookURL != "" }

// Event describes a completed job for notification purposes.
type Event struct {
	Job        string         `json:"job"`    // "backup", "prune", or a scheduled job name
	Status     string         `json:"status"` // "success" or "failure"
	Hostname   string         `json:"hostname"`
	SnapshotID string         `json:"snapshot_id,omitempty"`
	Stats      map[string]any `json:"stats,omitempty"`
	Error      string         `json:"error,omitempty"`
	Duration   time.Duration  `json:"duration_ns"`
	Time       time.Time      `json:"time"`
}

// durationSeconds is a convenience accessor used when building text payloads.
func (e Event) durationSeconds() float64 { return e.Duration.Seconds() }

// resolvedFormat returns the effective Format, inferring from the URL when
// Config.Format is unset.
func (c Config) resolvedFormat() Format {
	switch Format(strings.ToLower(c.Format)) {
	case FormatSlack:
		return FormatSlack
	case FormatDiscord:
		return FormatDiscord
	case FormatJSON:
		return FormatJSON
	}
	u := strings.ToLower(c.WebhookURL)
	switch {
	case strings.Contains(u, "hooks.slack.com"):
		return FormatSlack
	case strings.Contains(u, "discord.com") || strings.Contains(u, "discordapp.com"):
		return FormatDiscord
	default:
		return FormatJSON
	}
}

// Notifier sends Events to a configured webhook.
type Notifier struct {
	cfg    Config
	client *http.Client
}

// New returns a Notifier for cfg. If cfg is not Enabled(), the returned
// Notifier's Send is always a no-op (safe to call unconditionally).
func New(cfg Config) *Notifier {
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Notifier{
		cfg:    cfg,
		client: &http.Client{Timeout: timeout},
	}
}

// ShouldNotify reports whether an event with the given success flag should be
// sent, per cfg.OnSuccess/OnFailure.
func (n *Notifier) ShouldNotify(success bool) bool {
	if !n.cfg.Enabled() {
		return false
	}
	if success {
		return n.cfg.OnSuccess
	}
	return n.cfg.OnFailure
}

// Send posts ev to the configured webhook, honoring OnSuccess/OnFailure and
// Format. It never returns an error to force callers into error-handling
// gymnastics for what is inherently a best-effort side channel; instead it
// returns a non-nil error purely for logging/warning purposes, and callers
// are expected to log-and-continue, never abort the backup/prune on failure.
func (n *Notifier) Send(ctx context.Context, ev Event) error {
	if !n.cfg.Enabled() {
		return nil
	}
	success := ev.Status == "success"
	if !n.ShouldNotify(success) {
		return nil
	}

	body, contentType, err := buildPayload(n.cfg.resolvedFormat(), ev)
	if err != nil {
		return fmt.Errorf("notify: build payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.cfg.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("notify: build request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("notify: post webhook: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("notify: webhook returned status %d", resp.StatusCode)
	}
	return nil
}

// buildPayload marshals ev into the wire format for the given target.
func buildPayload(format Format, ev Event) ([]byte, string, error) {
	switch format {
	case FormatSlack:
		b, err := json.Marshal(struct {
			Text string `json:"text"`
		}{Text: textSummary(ev)})
		return b, "application/json", err
	case FormatDiscord:
		b, err := json.Marshal(struct {
			Content string `json:"content"`
		}{Content: textSummary(ev)})
		return b, "application/json", err
	default:
		b, err := json.Marshal(ev)
		return b, "application/json", err
	}
}

// textSummary renders a human-readable one-liner (plus optional error line)
// for chat-style webhooks (Slack/Discord).
func textSummary(ev Event) string {
	icon := ":white_check_mark:"
	if ev.Status != "success" {
		icon = ":x:"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s bakku %s %s on %s", icon, ev.Job, ev.Status, ev.Hostname)
	if ev.SnapshotID != "" {
		fmt.Fprintf(&sb, " (snapshot %s)", shortID(ev.SnapshotID))
	}
	fmt.Fprintf(&sb, " in %.1fs", ev.durationSeconds())
	if ev.Error != "" {
		fmt.Fprintf(&sb, "\nerror: %s", ev.Error)
	}
	return sb.String()
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
