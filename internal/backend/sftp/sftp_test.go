package sftp

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/pkg/sftp"
)

func TestParseURL(t *testing.T) {
	cases := []struct {
		in   string
		want ParsedURL
	}{
		{"sftp://alice@example.com/backups", ParsedURL{User: "alice", Host: "example.com", Port: "22", Path: "/backups"}},
		{"sftp://alice@example.com:2222/backups/x", ParsedURL{User: "alice", Host: "example.com", Port: "2222", Path: "/backups/x"}},
		{"sftp://example.com/backups", ParsedURL{User: "", Host: "example.com", Port: "22", Path: "/backups"}},
		{"sftp://example.com", ParsedURL{User: "", Host: "example.com", Port: "22", Path: "/"}},
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
	_, err := ParseURL("sftp://")
	if err == nil {
		t.Fatal("expected error for missing host")
	}
}

// newTestServerClient wires an in-memory sftp.Client to an in-memory
// sftp.Server over a net.Pipe, without any network or SSH handshake — this
// exercises the real pkg/sftp client/server protocol implementation
// end-to-end. The pkg/sftp server resolves absolute paths directly against
// the local OS filesystem (WithServerWorkingDirectory only affects relative
// paths), so the SFTP backend's root must be an absolute path such as a
// t.TempDir().
func newTestServerClient(t *testing.T) *sftp.Client {
	t.Helper()

	clientConn, serverConn := net.Pipe()

	server, err := sftp.NewServer(serverConn)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	go func() {
		server.Serve()
		server.Close()
	}()
	t.Cleanup(func() { server.Close() })

	client, err := sftp.NewClientPipe(clientConn, clientConn)
	if err != nil {
		t.Fatalf("NewClientPipe: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

func newTestBackend(t *testing.T) *SFTP {
	t.Helper()
	client := newTestServerClient(t)
	root := t.TempDir()
	b, err := newWithClient(client, root)
	if err != nil {
		t.Fatalf("newWithClient: %v", err)
	}
	return b
}

func TestSFTP_SaveLoadStatListDelete(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()

	content := []byte("hello sftp world, testing content")
	if err := b.Save(ctx, "data/ab/obj1", strings.NewReader(string(content)), int64(len(content))); err != nil {
		t.Fatalf("Save: %v", err)
	}

	size, err := b.Stat(ctx, "data/ab/obj1")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if size != int64(len(content)) {
		t.Fatalf("Stat size = %d, want %d", size, len(content))
	}

	rc, err := b.Load(ctx, "data/ab/obj1", 0, -1)
	if err != nil {
		t.Fatalf("Load full: %v", err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("Load full = %q, want %q", got, content)
	}

	rc, err = b.Load(ctx, "data/ab/obj1", 6, 5)
	if err != nil {
		t.Fatalf("Load range: %v", err)
	}
	got, err = io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatalf("ReadAll range: %v", err)
	}
	want := content[6:11]
	if string(got) != string(want) {
		t.Fatalf("Load range = %q, want %q", got, want)
	}

	if err := b.Save(ctx, "data/ab/obj2", strings.NewReader("second"), 6); err != nil {
		t.Fatalf("Save obj2: %v", err)
	}

	var found []string
	err = b.List(ctx, "data", func(key string, size int64) error {
		found = append(found, key)
		return nil
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("List found %d keys, want 2: %v", len(found), found)
	}

	if err := b.Delete(ctx, "data/ab/obj1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err = b.Stat(ctx, "data/ab/obj1")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat after delete: expected ErrNotExist, got %v", err)
	}

	if err := b.Delete(ctx, "data/ab/does-not-exist"); err != nil {
		t.Fatalf("Delete missing key: %v", err)
	}
}

func TestSFTP_LoadNotFound(t *testing.T) {
	b := newTestBackend(t)
	_, err := b.Load(context.Background(), "missing/key", 0, -1)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected ErrNotExist, got %v", err)
	}
}

func TestSFTP_StatNotFound(t *testing.T) {
	b := newTestBackend(t)
	_, err := b.Stat(context.Background(), "missing/key")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected ErrNotExist, got %v", err)
	}
}

func TestSFTP_RemotePath(t *testing.T) {
	b := &SFTP{root: "/data"}
	if got := b.remotePath("a/b"); got != "/data/a/b" {
		t.Fatalf("remotePath = %q, want %q", got, "/data/a/b")
	}
}

func TestHostKeyCallback_Insecure(t *testing.T) {
	t.Setenv("BAKKU_SSH_INSECURE", "1")
	cb, err := hostKeyCallback()
	if err != nil {
		t.Fatalf("hostKeyCallback: %v", err)
	}
	if cb == nil {
		t.Fatal("expected non-nil callback")
	}
}

func TestAuthMethods_Password(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("BAKKU_SFTP_PASSWORD", "secret")
	// Point HOME somewhere without ssh keys so only the password method is added.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	methods, err := authMethods()
	if err != nil {
		t.Fatalf("authMethods: %v", err)
	}
	if len(methods) != 1 {
		t.Fatalf("expected 1 auth method (password only), got %d", len(methods))
	}
}

func TestAuthMethods_NoneAvailable(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("BAKKU_SFTP_PASSWORD", "")
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	methods, err := authMethods()
	if err != nil {
		t.Fatalf("authMethods: %v", err)
	}
	if len(methods) != 0 {
		t.Fatalf("expected 0 auth methods, got %d", len(methods))
	}
}
