// Package smb implements a Backend backed by an SMB/CIFS share.
//
// URL form:
//
//	smb://user@host/share/path
//
// Authentication uses NTLMv2 with the password read from the
// BAKKU_SMB_PASSWORD environment variable. A "domain\user" or "domain;user"
// style username splits into Domain+User; otherwise Domain is left empty
// (falling back to whatever the server accepts as a workgroup default).
package smb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	smb2 "github.com/cloudsoda/go-smb2"

	"github.com/zephel01/bakku/internal/backend/retry"
)

// errNotExist mirrors backend.ErrNotExist without importing the parent
// package (which would create an import cycle).
var errNotExist = os.ErrNotExist

// SMB stores repository keys as files under a root directory on a shared
// SMB/CIFS volume.
type SMB struct {
	conn    net.Conn
	session *smb2.Session
	share   *smb2.Share
	root    string // '/'-separated path within the share, no leading/trailing slash
}

// ParsedURL holds the components extracted from an smb:// destination URL.
type ParsedURL struct {
	User  string
	Host  string
	Port  string
	Share string
	Path  string // '/'-separated, no leading/trailing slash
}

// ParseURL parses "smb://user@host/share/path". Port defaults to "445" if
// absent.
func ParseURL(raw string) (ParsedURL, error) {
	full := raw
	if !strings.Contains(full, "://") {
		full = "smb://" + full
	}
	u, err := url.Parse(full)
	if err != nil {
		return ParsedURL{}, fmt.Errorf("smb: invalid URL %q: %w", raw, err)
	}
	if u.Scheme != "" && u.Scheme != "smb" {
		return ParsedURL{}, fmt.Errorf("smb: unexpected scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return ParsedURL{}, fmt.Errorf("smb: missing host in %q", raw)
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "445"
	}
	user := ""
	if u.User != nil {
		user = u.User.Username()
	}
	trimmed := strings.Trim(u.Path, "/")
	if trimmed == "" {
		return ParsedURL{}, fmt.Errorf("smb: missing share name in %q", raw)
	}
	parts := strings.SplitN(trimmed, "/", 2)
	share := parts[0]
	p := ""
	if len(parts) > 1 {
		p = parts[1]
	}
	return ParsedURL{User: user, Host: host, Port: port, Share: share, Path: p}, nil
}

// splitDomainUser splits a "DOMAIN\user" or "DOMAIN;user" style username.
func splitDomainUser(u string) (domain, user string) {
	for _, sep := range []string{`\`, ";"} {
		if i := strings.Index(u, sep); i >= 0 {
			return u[:i], u[i+len(sep):]
		}
	}
	return "", u
}

// New constructs an SMB backend from a destination string of the form
// "smb://user@host/share/path".
func New(ctx context.Context, dst string) (*SMB, error) {
	pu, err := ParseURL(dst)
	if err != nil {
		return nil, err
	}

	password := os.Getenv("BAKKU_SMB_PASSWORD")
	domain, user := splitDomainUser(pu.User)

	dialer := &net.Dialer{Timeout: 30 * time.Second}
	addr := net.JoinHostPort(pu.Host, pu.Port)
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("smb: dial %s: %w", addr, err)
	}

	d := &smb2.Dialer{
		Initiator: &smb2.NTLMInitiator{
			User:     user,
			Password: password,
			Domain:   domain,
		},
	}
	session, err := d.DialConn(ctx, conn, addr)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("smb: session setup with %s: %w", addr, err)
	}

	share, err := session.Mount(pu.Share)
	if err != nil {
		session.Logoff()
		conn.Close()
		return nil, fmt.Errorf("smb: mounting share %q: %w", pu.Share, err)
	}

	root := strings.Trim(pu.Path, "/")
	s := &SMB{conn: conn, session: session, share: share, root: root}
	if root != "" {
		if err := s.mkdirAll(root); err != nil {
			share.Umount()
			session.Logoff()
			conn.Close()
			return nil, fmt.Errorf("smb: creating root %q: %w", root, err)
		}
	}
	return s, nil
}

// newWithShare wraps an already-mounted *smb2.Share (used by tests, or by
// callers that manage the connection lifecycle themselves).
func newWithShare(share *smb2.Share, root string) (*SMB, error) {
	s := &SMB{share: share, root: strings.Trim(root, "/")}
	if s.root != "" {
		if err := s.mkdirAll(s.root); err != nil {
			return nil, fmt.Errorf("smb: creating root %q: %w", s.root, err)
		}
	}
	return s, nil
}

func (s *SMB) smbPath(key string) string {
	key = strings.TrimPrefix(key, "/")
	p := path.Join(s.root, key)
	// go-smb2 uses backslash-separated Windows-style paths internally for
	// some operations but accepts forward slashes for Open/Stat/etc. We keep
	// forward slashes here since the library normalizes them.
	return p
}

// mkdirAll creates dir and all missing parents on the share.
func (s *SMB) mkdirAll(dir string) error {
	dir = strings.Trim(dir, "/")
	if dir == "" {
		return nil
	}
	parts := strings.Split(dir, "/")
	cur := ""
	for _, part := range parts {
		if cur == "" {
			cur = part
		} else {
			cur = cur + "/" + part
		}
		if _, err := s.share.Stat(cur); err == nil {
			continue
		}
		if err := s.share.Mkdir(cur, 0o755); err != nil {
			// Tolerate a race where another writer created it concurrently.
			if _, statErr := s.share.Stat(cur); statErr == nil {
				continue
			}
			return err
		}
	}
	return nil
}

// Save writes r to key atomically via a temp name + rename.
func (s *SMB) Save(ctx context.Context, key string, r io.Reader, size int64) error {
	dst := s.smbPath(key)
	dir := path.Dir(dst)

	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}

	return retry.Do(ctx, func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if dir != "." && dir != "/" {
			if err := s.mkdirAll(dir); err != nil {
				return fmt.Errorf("smb: mkdir %q: %w", dir, err)
			}
		}

		tmpName := fmt.Sprintf("%s.tmp-%d", dst, time.Now().UnixNano())
		f, err := s.share.Create(tmpName)
		if err != nil {
			return fmt.Errorf("smb: create %q: %w", tmpName, err)
		}
		if _, err := f.Write(data); err != nil {
			f.Close()
			s.share.Remove(tmpName)
			return fmt.Errorf("smb: write %q: %w", tmpName, err)
		}
		if err := f.Close(); err != nil {
			s.share.Remove(tmpName)
			return fmt.Errorf("smb: close %q: %w", tmpName, err)
		}
		if err := s.share.Rename(tmpName, dst); err != nil {
			s.share.Remove(dst)
			if err2 := s.share.Rename(tmpName, dst); err2 != nil {
				s.share.Remove(tmpName)
				return fmt.Errorf("smb: rename %q -> %q: %w", tmpName, dst, err2)
			}
		}
		return nil
	})
}

// Load returns a reader for [offset, offset+length) of key.
func (s *SMB) Load(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	p := s.smbPath(key)
	var f *smb2.File
	err := retry.Do(ctx, func(ctx context.Context) error {
		file, err := s.share.Open(p)
		if err != nil {
			if os.IsNotExist(err) || errors.Is(err, os.ErrNotExist) {
				return retry.Permanent(err)
			}
			return err
		}
		f = file
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) || errors.Is(err, os.ErrNotExist) {
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
	f *smb2.File
	r io.Reader
}

func (l *limitedFile) Read(p []byte) (int, error) { return l.r.Read(p) }
func (l *limitedFile) Close() error               { return l.f.Close() }

// Stat returns the size of key.
func (s *SMB) Stat(ctx context.Context, key string) (int64, error) {
	p := s.smbPath(key)
	var size int64
	err := retry.Do(ctx, func(ctx context.Context) error {
		fi, err := s.share.Stat(p)
		if err != nil {
			if os.IsNotExist(err) || errors.Is(err, os.ErrNotExist) {
				return retry.Permanent(err)
			}
			return err
		}
		size = fi.Size()
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) || errors.Is(err, os.ErrNotExist) {
			return 0, fmt.Errorf("%w: %s", errNotExist, key)
		}
		return 0, err
	}
	return size, nil
}

// List calls fn for every regular file under prefix, recursing into
// subdirectories.
func (s *SMB) List(ctx context.Context, prefix string, fn func(key string, size int64) error) error {
	base := s.smbPath(prefix)
	return s.walk(ctx, base, fn)
}

func (s *SMB) walk(ctx context.Context, dir string, fn func(key string, size int64) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := s.share.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) || errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		full := dir + "/" + e.Name()
		if e.IsDir() {
			if err := s.walk(ctx, full, fn); err != nil {
				return err
			}
			continue
		}
		if strings.Contains(e.Name(), ".tmp-") {
			continue
		}
		rel := strings.TrimPrefix(full, s.root)
		rel = strings.TrimPrefix(rel, "/")
		if err := fn(rel, e.Size()); err != nil {
			return err
		}
	}
	return nil
}

// Delete removes key. A missing key is not an error.
func (s *SMB) Delete(ctx context.Context, key string) error {
	p := s.smbPath(key)
	return retry.Do(ctx, func(ctx context.Context) error {
		err := s.share.Remove(p)
		if err != nil && !os.IsNotExist(err) && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	})
}

// Close unmounts the share and closes the underlying session/connection.
func (s *SMB) Close() error {
	var err error
	if s.share != nil {
		err = s.share.Umount()
	}
	if s.session != nil {
		if e := s.session.Logoff(); e != nil && err == nil {
			err = e
		}
	}
	if s.conn != nil {
		if e := s.conn.Close(); e != nil && err == nil {
			err = e
		}
	}
	return err
}
