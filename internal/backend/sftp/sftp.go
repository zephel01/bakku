// Package sftp implements a Backend backed by a remote directory reachable
// over SFTP.
//
// URL form:
//
//	sftp://user@host:port/path
//
// Authentication is attempted, in order:
//  1. ssh-agent, if SSH_AUTH_SOCK is set.
//  2. Private keys ~/.ssh/id_ed25519 and ~/.ssh/id_rsa.
//  3. Password from the BAKKU_SFTP_PASSWORD environment variable.
//
// Host keys are verified against ~/.ssh/known_hosts unless
// BAKKU_SSH_INSECURE=1 is set, in which case host key verification is
// skipped entirely (useful for tests/throwaway hosts, NOT recommended for
// production).
package sftp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/pkg/sftp"

	"github.com/zephel01/bakku/internal/backend/keyguard"
	"github.com/zephel01/bakku/internal/backend/retry"
)

// errNotExist mirrors backend.ErrNotExist without importing the parent
// package (which would create an import cycle).
var errNotExist = os.ErrNotExist

// SFTP stores repository keys as files under a root directory on a remote
// host reachable over SFTP.
type SFTP struct {
	mu       sync.Mutex
	sshConn  *ssh.Client
	client   *sftp.Client
	root     string // '/'-separated remote path
	dialOnce func() (*ssh.Client, *sftp.Client, error)
}

// ParsedURL holds the components extracted from an sftp:// destination URL.
type ParsedURL struct {
	User string
	Host string
	Port string
	Path string
}

// ParseURL parses "sftp://user@host:port/path". Port defaults to "22" if
// absent; User defaults to the current OS user if absent.
func ParseURL(raw string) (ParsedURL, error) {
	full := raw
	if !strings.Contains(full, "://") {
		full = "sftp://" + full
	}
	u, err := url.Parse(full)
	if err != nil {
		return ParsedURL{}, fmt.Errorf("sftp: invalid URL %q: %w", raw, err)
	}
	if u.Scheme != "" && u.Scheme != "sftp" {
		return ParsedURL{}, fmt.Errorf("sftp: unexpected scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return ParsedURL{}, fmt.Errorf("sftp: missing host in %q", raw)
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "22"
	}
	user := ""
	if u.User != nil {
		user = u.User.Username()
	}
	p := u.Path
	if p == "" {
		p = "/"
	}
	return ParsedURL{User: user, Host: host, Port: port, Path: p}, nil
}

// New constructs an SFTP backend from a destination string of the form
// "sftp://user@host:port/path".
func New(ctx context.Context, dst string) (*SFTP, error) {
	pu, err := ParseURL(dst)
	if err != nil {
		return nil, err
	}

	sshConn, sftpClient, err := dial(ctx, pu)
	if err != nil {
		return nil, err
	}

	root := pu.Path
	if root == "" {
		root = "/"
	}
	if err := sftpClient.MkdirAll(root); err != nil {
		sftpClient.Close()
		sshConn.Close()
		return nil, fmt.Errorf("sftp: creating root %q: %w", root, err)
	}

	return &SFTP{sshConn: sshConn, client: sftpClient, root: root}, nil
}

// newWithClient wraps an already-established sftp.Client (used by tests to
// inject an in-memory client/server pipe).
func newWithClient(client *sftp.Client, root string) (*SFTP, error) {
	if err := client.MkdirAll(root); err != nil {
		return nil, fmt.Errorf("sftp: creating root %q: %w", root, err)
	}
	return &SFTP{client: client, root: root}, nil
}

func dial(ctx context.Context, pu ParsedURL) (*ssh.Client, *sftp.Client, error) {
	user := pu.User
	if user == "" {
		if u, err := currentUser(); err == nil {
			user = u
		}
	}

	auths, err := authMethods()
	if err != nil {
		return nil, nil, err
	}
	if len(auths) == 0 {
		return nil, nil, errors.New("sftp: no authentication method available (need ssh-agent, ~/.ssh key, or BAKKU_SFTP_PASSWORD)")
	}

	hostKeyCallback, err := hostKeyCallback()
	if err != nil {
		return nil, nil, err
	}

	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            auths,
		HostKeyCallback: hostKeyCallback,
		Timeout:         30 * time.Second,
	}

	addr := net.JoinHostPort(pu.Host, pu.Port)

	dialer := &net.Dialer{Timeout: 30 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("sftp: dial %s: %w", addr, err)
	}

	sshConnRaw, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("sftp: ssh handshake with %s: %w", addr, err)
	}
	sshConn := ssh.NewClient(sshConnRaw, chans, reqs)

	sftpClient, err := sftp.NewClient(sshConn)
	if err != nil {
		sshConn.Close()
		return nil, nil, fmt.Errorf("sftp: starting sftp subsystem: %w", err)
	}

	return sshConn, sftpClient, nil
}

func currentUser() (string, error) {
	if u := os.Getenv("USER"); u != "" {
		return u, nil
	}
	return "", errors.New("sftp: cannot determine current user")
}

// authMethods builds the ssh.AuthMethod chain: ssh-agent, then private keys,
// then password from BAKKU_SFTP_PASSWORD.
func authMethods() ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if conn, err := net.Dial("unix", sock); err == nil {
			ag := agent.NewClient(conn)
			methods = append(methods, ssh.PublicKeysCallback(ag.Signers))
		}
	}

	if signers := loadKeySigners(); len(signers) > 0 {
		methods = append(methods, ssh.PublicKeys(signers...))
	}

	if pw := os.Getenv("BAKKU_SFTP_PASSWORD"); pw != "" {
		methods = append(methods, ssh.Password(pw))
	}

	return methods, nil
}

func loadKeySigners() []ssh.Signer {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var signers []ssh.Signer
	for _, name := range []string{"id_ed25519", "id_rsa"} {
		p := filepath.Join(home, ".ssh", name)
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		signer, err := ssh.ParsePrivateKey(data)
		if err != nil {
			continue
		}
		signers = append(signers, signer)
	}
	return signers
}

// hostKeyCallback returns a callback that verifies against ~/.ssh/known_hosts,
// unless BAKKU_SSH_INSECURE=1 is set.
func hostKeyCallback() (ssh.HostKeyCallback, error) {
	if os.Getenv("BAKKU_SSH_INSECURE") == "1" {
		fmt.Fprintln(os.Stderr, "bakku: WARNING: BAKKU_SSH_INSECURE=1 disables SSH host key verification; "+
			"the connection is exposed to man-in-the-middle attacks and any password/key may be sent to an impostor server. "+
			"Add the host to ~/.ssh/known_hosts and unset BAKKU_SSH_INSECURE for a secure connection.")
		return ssh.InsecureIgnoreHostKey(), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("sftp: cannot locate known_hosts (no home dir): %w", err)
	}
	khPath := filepath.Join(home, ".ssh", "known_hosts")
	cb, err := knownhosts.New(khPath)
	if err != nil {
		return nil, fmt.Errorf("sftp: loading known_hosts %q: %w (set BAKKU_SSH_INSECURE=1 to skip verification)", khPath, err)
	}
	return cb, nil
}

func (s *SFTP) remotePath(key string) string {
	key = strings.TrimPrefix(key, "/")
	return path.Join(s.root, key)
}

// Save writes r to key atomically by uploading to a temp name and renaming.
func (s *SFTP) Save(ctx context.Context, key string, r io.Reader, size int64) error {
	if err := keyguard.Validate(key); err != nil {
		return err
	}
	dst := s.remotePath(key)
	dir := path.Dir(dst)

	// Buffer the payload so retries can replay it (SFTP uploads aren't
	// naturally resumable across a failed attempt without re-reading from
	// the start).
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}

	return retry.Do(ctx, func(ctx context.Context) error {
		s.mu.Lock()
		defer s.mu.Unlock()

		if err := ctx.Err(); err != nil {
			return err
		}

		if err := s.client.MkdirAll(dir); err != nil {
			return fmt.Errorf("sftp: mkdir %q: %w", dir, err)
		}

		tmpName := fmt.Sprintf("%s.tmp-%d", dst, time.Now().UnixNano())
		f, err := s.client.Create(tmpName)
		if err != nil {
			return fmt.Errorf("sftp: create %q: %w", tmpName, err)
		}
		if _, err := f.Write(data); err != nil {
			f.Close()
			s.client.Remove(tmpName)
			return fmt.Errorf("sftp: write %q: %w", tmpName, err)
		}
		if err := f.Close(); err != nil {
			s.client.Remove(tmpName)
			return fmt.Errorf("sftp: close %q: %w", tmpName, err)
		}
		if err := s.client.Rename(tmpName, dst); err != nil {
			// Some servers require the destination to not exist for Rename;
			// remove and retry the rename once.
			s.client.Remove(dst)
			if err2 := s.client.Rename(tmpName, dst); err2 != nil {
				s.client.Remove(tmpName)
				return fmt.Errorf("sftp: rename %q -> %q: %w", tmpName, dst, err2)
			}
		}
		return nil
	})
}

// Load returns a reader for [offset, offset+length) of key.
func (s *SFTP) Load(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	if err := keyguard.Validate(key); err != nil {
		return nil, err
	}
	p := s.remotePath(key)
	var f *sftp.File
	err := retry.Do(ctx, func(ctx context.Context) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		file, err := s.client.Open(p)
		if err != nil {
			if os.IsNotExist(err) {
				return retry.Permanent(err)
			}
			return err
		}
		f = file
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", errNotExist, key)
		}
		return nil, err
	}

	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			f.Close()
			return nil, err
		}
	}
	if length < 0 {
		return f, nil
	}
	return &limitedFile{f: f, r: io.LimitReader(f, length)}, nil
}

type limitedFile struct {
	f *sftp.File
	r io.Reader
}

func (l *limitedFile) Read(p []byte) (int, error) { return l.r.Read(p) }
func (l *limitedFile) Close() error               { return l.f.Close() }

// Stat returns the size of key.
func (s *SFTP) Stat(ctx context.Context, key string) (int64, error) {
	if err := keyguard.Validate(key); err != nil {
		return 0, err
	}
	p := s.remotePath(key)
	var size int64
	err := retry.Do(ctx, func(ctx context.Context) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		fi, err := s.client.Stat(p)
		if err != nil {
			if os.IsNotExist(err) {
				return retry.Permanent(err)
			}
			return err
		}
		size = fi.Size()
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return 0, fmt.Errorf("%w: %s", errNotExist, key)
		}
		return 0, err
	}
	return size, nil
}

// List calls fn for every regular file under prefix.
func (s *SFTP) List(ctx context.Context, prefix string, fn func(key string, size int64) error) error {
	if err := keyguard.Validate(prefix); err != nil {
		return err
	}
	base := s.remotePath(prefix)

	s.mu.Lock()
	walker := s.client.Walk(base)
	s.mu.Unlock()

	for walker.Step() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := walker.Err(); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		info := walker.Stat()
		if info.IsDir() {
			continue
		}
		name := path.Base(walker.Path())
		if strings.Contains(name, ".tmp-") {
			continue
		}
		rel := strings.TrimPrefix(walker.Path(), s.root)
		rel = strings.TrimPrefix(rel, "/")
		if err := fn(rel, info.Size()); err != nil {
			return err
		}
	}
	return nil
}

// Delete removes key. A missing key is not an error.
func (s *SFTP) Delete(ctx context.Context, key string) error {
	if err := keyguard.Validate(key); err != nil {
		return err
	}
	p := s.remotePath(key)
	return retry.Do(ctx, func(ctx context.Context) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		err := s.client.Remove(p)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	})
}

// Close closes the underlying SFTP and SSH connections.
func (s *SFTP) Close() error {
	var err error
	if s.client != nil {
		err = s.client.Close()
	}
	if s.sshConn != nil {
		if e := s.sshConn.Close(); e != nil && err == nil {
			err = e
		}
	}
	return err
}
