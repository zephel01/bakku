package smb

import (
	"context"
	"testing"
	"time"
)

func TestParseURL(t *testing.T) {
	cases := []struct {
		in   string
		want ParsedURL
	}{
		{
			in:   "smb://alice@fileserver/backups/prod",
			want: ParsedURL{User: "alice", Host: "fileserver", Port: "445", Share: "backups", Path: "prod"},
		},
		{
			in:   "smb://alice@fileserver:1445/backups/prod/sub",
			want: ParsedURL{User: "alice", Host: "fileserver", Port: "1445", Share: "backups", Path: "prod/sub"},
		},
		{
			in:   "smb://fileserver/backups",
			want: ParsedURL{User: "", Host: "fileserver", Port: "445", Share: "backups", Path: ""},
		},
	}
	for _, c := range cases {
		got, err := ParseURL(c.in)
		if err != nil {
			t.Fatalf("ParseURL(%q) error: %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("ParseURL(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestParseURL_MissingHost(t *testing.T) {
	_, err := ParseURL("smb://")
	if err == nil {
		t.Fatal("expected error for missing host")
	}
}

func TestParseURL_MissingShare(t *testing.T) {
	_, err := ParseURL("smb://fileserver")
	if err == nil {
		t.Fatal("expected error for missing share name")
	}
	_, err = ParseURL("smb://fileserver/")
	if err == nil {
		t.Fatal("expected error for missing share name (trailing slash)")
	}
}

func TestSplitDomainUser(t *testing.T) {
	cases := []struct {
		in         string
		wantDomain string
		wantUser   string
	}{
		{`WORKGROUP\alice`, "WORKGROUP", "alice"},
		{"WORKGROUP;alice", "WORKGROUP", "alice"},
		{"alice", "", "alice"},
		{"", "", ""},
	}
	for _, c := range cases {
		domain, user := splitDomainUser(c.in)
		if domain != c.wantDomain || user != c.wantUser {
			t.Fatalf("splitDomainUser(%q) = (%q,%q), want (%q,%q)", c.in, domain, user, c.wantDomain, c.wantUser)
		}
	}
}

// TestNew_DialFailure exercises the error path when the target host is
// unreachable, without requiring a real SMB server. A short-lived context
// combined with a non-routable address ensures this fails fast rather than
// hanging.
func TestNew_DialFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, err := New(ctx, "smb://baduser@127.0.0.1:1/share/path")
	if err == nil {
		t.Fatal("expected error connecting to an unreachable SMB host")
	}
}

func TestSMBPath(t *testing.T) {
	s := &SMB{root: "data"}
	if got := s.smbPath("ab/cd"); got != "data/ab/cd" {
		t.Fatalf("smbPath = %q, want %q", got, "data/ab/cd")
	}
	s2 := &SMB{root: ""}
	if got := s2.smbPath("ab/cd"); got != "ab/cd" {
		t.Fatalf("smbPath (no root) = %q, want %q", got, "ab/cd")
	}
}
