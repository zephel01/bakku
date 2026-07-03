package backend

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestSplitScheme(t *testing.T) {
	cases := []struct {
		in         string
		wantScheme string
		wantRest   string
	}{
		{"s3://bucket/prefix", "s3", "bucket/prefix"},
		{"sftp://user@host/path", "sftp", "user@host/path"},
		{"gdrive://folder", "gdrive", "folder"},
		{"dropbox://path", "dropbox", "path"},
		{"smb://user@host/share", "smb", "user@host/share"},
		{"/abs/path", "", "/abs/path"},
		{"./rel/path", "", "./rel/path"},
		{"file:///abs/path", "file", "/abs/path"},
	}
	for _, c := range cases {
		scheme, rest := splitScheme(c.in)
		if scheme != c.wantScheme || rest != c.wantRest {
			t.Fatalf("splitScheme(%q) = (%q,%q), want (%q,%q)", c.in, scheme, rest, c.wantScheme, c.wantRest)
		}
	}
}

// TestOpen_UnknownScheme verifies Open still rejects schemes it doesn't
// recognize at all (distinct from the five remote schemes it now routes to
// real backend constructors).
func TestOpen_UnknownScheme(t *testing.T) {
	_, err := Open(context.Background(), "ftp://example.com/path", Options{})
	if err == nil {
		t.Fatal("expected error for unknown scheme")
	}
}

// TestOpen_RoutesRemoteSchemes verifies each remote scheme reaches its
// corresponding backend constructor (rather than falling through to
// "unknown scheme" or the local filesystem path), by checking that the
// error surfaced is the scheme-specific configuration error, not a generic
// "unknown scheme" error. Each subpackage's own tests cover its behavior in
// depth; this only proves the switch in Open wires up correctly.
func TestOpen_RoutesRemoteSchemes(t *testing.T) {
	t.Setenv("BAKKU_GDRIVE_CREDENTIALS", "")
	t.Setenv("BAKKU_DROPBOX_TOKEN", "")

	cases := []struct {
		name string
		dst  string
	}{
		{"gdrive", "gdrive://some-folder"},
		{"dropbox", "dropbox://some-path"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Open(context.Background(), c.dst, Options{})
			if err == nil {
				t.Fatalf("expected configuration error for %s (no credentials set)", c.name)
			}
			if isUnknownScheme(err) {
				t.Fatalf("scheme %s was not routed to its backend: %v", c.name, err)
			}
		})
	}
}

// TestOpen_SFTP_SMB_DialFailFast verifies sftp:// and smb:// are routed to
// their constructors (which attempt a network dial) rather than treated as
// unknown schemes, using an unroutable address plus a short context timeout
// so the test fails fast instead of hanging or requiring network access.
func TestOpen_SFTP_SMB_DialFailFast(t *testing.T) {
	cases := []string{
		"sftp://user@127.0.0.1:1/path",
		"smb://user@127.0.0.1:1/share/path",
	}
	for _, dst := range cases {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		_, err := Open(ctx, dst, Options{})
		cancel()
		if err == nil {
			t.Fatalf("expected dial error for %q", dst)
		}
		if isUnknownScheme(err) {
			t.Fatalf("%q was not routed to its backend: %v", dst, err)
		}
	}
}

func isUnknownScheme(err error) bool {
	return err != nil && strings.Contains(err.Error(), "unknown scheme")
}
