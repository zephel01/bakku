// Package backend defines the storage abstraction bakku writes repositories to,
// and a factory that constructs a concrete backend from a URL or path.
package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/zephel01/bakku/internal/backend/dropbox"
	"github.com/zephel01/bakku/internal/backend/gdrive"
	"github.com/zephel01/bakku/internal/backend/local"
	"github.com/zephel01/bakku/internal/backend/s3"
	"github.com/zephel01/bakku/internal/backend/sftp"
	"github.com/zephel01/bakku/internal/backend/smb"
)

// ErrNotExist is returned (wrapped) by Load/Stat when the key does not exist.
// It aliases os.ErrNotExist so errors.Is(err, backend.ErrNotExist) and
// errors.Is(err, os.ErrNotExist) are equivalent regardless of which backend
// produced the error.
var ErrNotExist = os.ErrNotExist

// IsNotExist reports whether err indicates a missing key.
func IsNotExist(err error) bool { return errors.Is(err, ErrNotExist) }

// Backend is the storage abstraction. Keys are '/'-separated paths within a
// flat-ish namespace (e.g. "data/ab/<packid>"). Implementations must be safe
// for concurrent use by multiple goroutines.
type Backend interface {
	// Save stores r under key. size is the number of bytes in r (or -1 if
	// unknown). Save must be atomic: a key either fully exists or not at all.
	Save(ctx context.Context, key string, r io.Reader, size int64) error
	// Load returns a reader for [offset, offset+length) of key. length<0 means
	// "until end". The caller must Close the returned reader.
	Load(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error)
	// Stat returns the size of key in bytes, or ErrNotExist.
	Stat(ctx context.Context, key string) (int64, error)
	// List calls fn for every key under prefix. Iteration stops (and the error
	// is returned) if fn returns non-nil.
	List(ctx context.Context, prefix string, fn func(key string, size int64) error) error
	// Delete removes key. Deleting a missing key is not an error.
	Delete(ctx context.Context, key string) error
	// Close releases any resources held by the backend.
	Close() error
}

// Options carry optional backend construction parameters (credentials, etc.).
// Phase 1-2 only needs a subset; later backends read more fields.
type Options struct {
	// Extra holds backend-specific key/value options parsed from config.
	Extra map[string]string
}

// Open constructs a Backend from a destination string. Supported forms:
//
//	file:///abs/path                                   -> local filesystem
//	/abs/path or ./rel                                 -> local filesystem (plain path)
//	s3://bucket/prefix?endpoint=...&region=...          -> Amazon S3 / S3-compatible
//	sftp://user@host:port/path                          -> SFTP
//	gdrive://folder-path                                -> Google Drive
//	dropbox://path                                      -> Dropbox
//	smb://user@host/share/path                          -> SMB/CIFS share
//
// See the respective backend subpackages (internal/backend/{s3,sftp,gdrive,
// dropbox,smb}) for the environment variables and authentication each
// remote scheme requires.
func Open(ctx context.Context, dst string, opts Options) (Backend, error) {
	_ = opts
	scheme, rest := splitScheme(dst)
	switch scheme {
	case "", "file":
		path := rest
		if scheme == "file" {
			// file:// URLs: strip the (empty) host component.
			u, err := url.Parse(dst)
			if err != nil {
				return nil, fmt.Errorf("backend: invalid file URL %q: %w", dst, err)
			}
			path = u.Path
		}
		return local.New(path)
	case "s3":
		return s3.New(ctx, dst)
	case "sftp":
		return sftp.New(ctx, dst)
	case "gdrive":
		return gdrive.New(ctx, dst)
	case "dropbox":
		return dropbox.New(ctx, dst)
	case "smb":
		return smb.New(ctx, dst)
	default:
		return nil, fmt.Errorf("backend: unknown scheme %q", scheme)
	}
}

// splitScheme splits "scheme://rest" into ("scheme", "rest"). A plain path with
// no "://" yields ("", path).
func splitScheme(s string) (scheme, rest string) {
	if i := strings.Index(s, "://"); i >= 0 {
		return s[:i], s[i+3:]
	}
	return "", s
}
