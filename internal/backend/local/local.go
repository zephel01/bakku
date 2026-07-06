// Package local implements a filesystem-backed Backend.
package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// errNotExist mirrors backend.ErrNotExist without importing the parent package
// (which would create an import cycle). The parent maps os.ErrNotExist too, so
// callers should test with errors.Is(err, os.ErrNotExist) OR backend.ErrNotExist.
var errNotExist = os.ErrNotExist

// Local stores repository keys as files under a root directory. A key such as
// "data/ab/cd" maps to <root>/data/ab/cd.
type Local struct {
	root string
}

// New returns a Local backend rooted at dir, creating dir if necessary.
func New(dir string) (*Local, error) {
	if dir == "" {
		return nil, errors.New("local: empty path")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	// 0700: a repository holds encrypted blobs, but key names, object sizes and
	// the overall structure are metadata that should not leak to other local
	// users. Restrict the repo tree to the owner.
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, err
	}
	return &Local{root: abs}, nil
}

func (l *Local) path(key string) string {
	// Keys use '/'; convert to OS separators for the filesystem.
	return filepath.Join(l.root, filepath.FromSlash(key))
}

// Save writes r to key atomically via a temp file + rename.
func (l *Local) Save(ctx context.Context, key string, r io.Reader, size int64) error {
	_ = ctx
	_ = size
	dst := l.path(key)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}
	if _, err := io.Copy(tmp, r); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// Load opens key and seeks to offset, returning a reader limited to length
// bytes (or to EOF if length<0).
func (l *Local) Load(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	_ = ctx
	f, err := os.Open(l.path(key))
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

// limitedFile pairs a LimitReader with the underlying file so Close works.
type limitedFile struct {
	f *os.File
	r io.Reader
}

func (l *limitedFile) Read(p []byte) (int, error) { return l.r.Read(p) }
func (l *limitedFile) Close() error               { return l.f.Close() }

// Stat returns the size of key.
func (l *Local) Stat(ctx context.Context, key string) (int64, error) {
	_ = ctx
	fi, err := os.Stat(l.path(key))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, fmt.Errorf("%w: %s", errNotExist, key)
		}
		return 0, err
	}
	return fi.Size(), nil
}

// List walks the tree under prefix and calls fn for each regular file, with the
// '/'-separated key relative to root.
func (l *Local) List(ctx context.Context, prefix string, fn func(key string, size int64) error) error {
	base := l.path(prefix)
	return filepath.Walk(base, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			// A missing prefix directory simply yields no entries.
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if info.IsDir() {
			return nil
		}
		name := filepath.Base(p)
		if strings.HasPrefix(name, ".tmp-") {
			return nil // skip in-flight temp files
		}
		rel, err := filepath.Rel(l.root, p)
		if err != nil {
			return err
		}
		return fn(filepath.ToSlash(rel), info.Size())
	})
}

// Delete removes key. A missing key is not an error.
func (l *Local) Delete(ctx context.Context, key string) error {
	_ = ctx
	err := os.Remove(l.path(key))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Close is a no-op for the local backend.
func (l *Local) Close() error { return nil }
