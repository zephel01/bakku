package notify

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// TestMain enables the private-address opt-out for the whole notify test
// binary, because the httptest servers used by the delivery tests listen on
// 127.0.0.1 which the SSRF guard blocks by default. The guard itself is
// exercised directly (env-independent) by the tests below.
func TestMain(m *testing.M) {
	os.Setenv("BAKKU_NOTIFY_ALLOW_PRIVATE", "1")
	os.Exit(m.Run())
}

func TestIsBlockedIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1",       // loopback
		"::1",             // loopback v6
		"169.254.169.254", // cloud metadata (link-local)
		"10.1.2.3",        // RFC1918
		"192.168.0.5",     // RFC1918
		"172.16.9.9",      // RFC1918
		"0.0.0.0",         // unspecified
		"fc00::1",         // ULA
	}
	for _, s := range blocked {
		if !isBlockedIP(net.ParseIP(s)) {
			t.Errorf("isBlockedIP(%s) = false, want true", s)
		}
	}
	allowed := []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "2606:4700:4700::1111"}
	for _, s := range allowed {
		if isBlockedIP(net.ParseIP(s)) {
			t.Errorf("isBlockedIP(%s) = true, want false", s)
		}
	}
}

func TestValidateWebhookURL(t *testing.T) {
	good := []string{"https://hooks.slack.com/x", "http://example.com/hook"}
	for _, u := range good {
		if err := ValidateWebhookURL(u); err != nil {
			t.Errorf("ValidateWebhookURL(%q) = %v, want nil", u, err)
		}
	}
	bad := []string{"", "ftp://example.com", "file:///etc/passwd", "gopher://x", "https://"}
	for _, u := range bad {
		if err := ValidateWebhookURL(u); err == nil {
			t.Errorf("ValidateWebhookURL(%q) = nil, want error", u)
		}
	}
}

// TestSafeClientBlocksLoopback verifies the connect-time guard refuses a
// loopback destination when private addresses are NOT allowed, regardless of
// the process-wide opt-out set in TestMain.
func TestSafeClientBlocksLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := newSafeClient(5*time.Second, false) // guard active
	req, _ := http.NewRequest(http.MethodPost, srv.URL, nil)
	if _, err := client.Do(req); err == nil {
		t.Fatal("expected SSRF guard to block loopback connection, got nil error")
	}

	client2 := newSafeClient(5*time.Second, true) // guard disabled
	req2, _ := http.NewRequest(http.MethodPost, srv.URL, nil)
	resp, err := client2.Do(req2)
	if err != nil {
		t.Fatalf("allowPrivate client should reach loopback: %v", err)
	}
	resp.Body.Close()
}
