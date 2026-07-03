package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestEvent(status string) Event {
	return Event{
		Job:        "backup",
		Status:     status,
		Hostname:   "test-host",
		SnapshotID: "abcdef1234567890",
		Stats:      map[string]any{"files_new": float64(3)},
		Duration:   2500 * time.Millisecond,
		Time:       time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC),
	}
}

func TestSendJSONPayload(t *testing.T) {
	var gotBody []byte
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := New(Config{WebhookURL: srv.URL, OnSuccess: true, OnFailure: true, Format: "json"})
	if err := n.Send(context.Background(), newTestEvent("success")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotContentType != "application/json" {
		t.Errorf("content-type = %q, want application/json", gotContentType)
	}
	var decoded Event
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Fatalf("unmarshal posted body: %v", err)
	}
	if decoded.Job != "backup" || decoded.Status != "success" || decoded.SnapshotID != "abcdef1234567890" {
		t.Errorf("decoded event mismatch: %+v", decoded)
	}
	if decoded.Hostname != "test-host" {
		t.Errorf("hostname = %q, want test-host", decoded.Hostname)
	}
}

func TestSendSlackPayload(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := New(Config{WebhookURL: srv.URL, OnSuccess: true, Format: "slack"})
	if err := n.Send(context.Background(), newTestEvent("success")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	var payload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("unmarshal slack payload: %v", err)
	}
	if payload.Text == "" {
		t.Fatal("slack payload text is empty")
	}
	wantSubstr := "bakku backup success on test-host"
	if !contains(payload.Text, wantSubstr) {
		t.Errorf("slack text = %q, want it to contain %q", payload.Text, wantSubstr)
	}
}

func TestSendDiscordPayload(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := New(Config{WebhookURL: srv.URL, OnFailure: true, Format: "discord"})
	ev := newTestEvent("failure")
	ev.Error = "boom"
	if err := n.Send(context.Background(), ev); err != nil {
		t.Fatalf("Send: %v", err)
	}
	var payload struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("unmarshal discord payload: %v", err)
	}
	if !contains(payload.Content, "boom") {
		t.Errorf("discord content = %q, want it to contain the error", payload.Content)
	}
}

func TestFormatInferredFromURL(t *testing.T) {
	cases := []struct {
		url  string
		want Format
	}{
		{"https://hooks.slack.com/services/x/y/z", FormatSlack},
		{"https://discord.com/api/webhooks/x/y", FormatDiscord},
		{"https://discordapp.com/api/webhooks/x/y", FormatDiscord},
		{"https://example.com/webhook", FormatJSON},
	}
	for _, c := range cases {
		cfg := Config{WebhookURL: c.url}
		if got := cfg.resolvedFormat(); got != c.want {
			t.Errorf("resolvedFormat(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}

func TestShouldNotifyRespectsOnSuccessOnFailure(t *testing.T) {
	n := New(Config{WebhookURL: "https://example.com/hook", OnSuccess: true, OnFailure: false})
	if !n.ShouldNotify(true) {
		t.Error("expected ShouldNotify(true) to be true")
	}
	if n.ShouldNotify(false) {
		t.Error("expected ShouldNotify(false) to be false")
	}
}

func TestSendNoOpWhenDisabled(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	// No webhook URL configured at all.
	n := New(Config{})
	if err := n.Send(context.Background(), newTestEvent("success")); err != nil {
		t.Fatalf("Send on disabled config should be a no-op, got error: %v", err)
	}
	if called {
		t.Fatal("webhook should not have been called when notify is not Enabled()")
	}

	// Configured but the relevant OnSuccess/OnFailure flag is off.
	n2 := New(Config{WebhookURL: srv.URL, OnSuccess: false, OnFailure: false})
	if err := n2.Send(context.Background(), newTestEvent("success")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if called {
		t.Fatal("webhook should not have been called when OnSuccess/OnFailure are both false")
	}
}

func TestSendNonFatalOnHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	n := New(Config{WebhookURL: srv.URL, OnSuccess: true})
	err := n.Send(context.Background(), newTestEvent("success"))
	if err == nil {
		t.Fatal("expected Send to return an error for a 500 response")
	}
	// The important contract (exercised by callers in internal/cli) is that
	// this error is advisory only -- it must never be wrapped in a way that
	// looks like anything other than a plain error a caller can log.
	if err.Error() == "" {
		t.Fatal("expected a non-empty error message")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
