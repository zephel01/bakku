// Package fs provides OS-specific filesystem metadata helpers used by the
// archiver and restorer: ownership (uid/gid), extended attributes, hard-link
// detection, and (Windows) long-path / share-delete file access.
//
// Every exported function has a per-OS implementation (build tags _linux.go,
// _darwin.go, _windows.go) plus a portable fallback (_other.go) so the archiver
// and restorer can call these unconditionally without OS-specific branching.
// Unsupported platforms return zero values / nil / ErrNotSupported so callers
// can treat them as "nothing to do".
package fs

import (
	"errors"
	"os"

	"github.com/zephel01/bakku/internal/repo"
)

// ErrNotSupported is returned by operations that have no meaningful
// implementation on the current OS.
var ErrNotSupported = errors.New("fs: not supported on this platform")

// OwnerInfo is the POSIX ownership of a filesystem entry.
type OwnerInfo struct {
	UID uint32
	GID uint32
}

// LinkKey identifies a hard-linkable filesystem object (inode+device) so the
// archiver can detect multiple names for the same file within one walk.
type LinkKey struct {
	Dev   uint64
	Inode uint64
}

// RestoreOptions controls best-effort metadata restoration.
type RestoreOptions struct {
	// Chown restores uid/gid; only meaningful (and normally only permitted) when
	// running as root/Administrator. Failures are treated as warnings by the
	// caller, never fatal.
	Chown bool
	// RestoreQuarantine, when false (default), skips restoring the macOS
	// com.apple.quarantine xattr even if it was captured.
	RestoreQuarantine bool
}

// Collect gathers OS metadata (ownership, xattrs, hardlink key) for path given
// its already-Lstat'd os.FileInfo. It never returns an error for missing
// metadata support; unsupported fields are simply left zero.
func Collect(path string, fi os.FileInfo) (owner OwnerInfo, hasOwner bool, xattrs map[string][]byte, link LinkKey, hasLink bool) {
	return collect(path, fi)
}

// ApplyOwnerAndXattrs restores ownership and xattrs onto an already-written
// path, honoring opts. Errors are non-fatal by convention; callers should log
// a warning and continue. Returns a slice of warning strings (empty on full
// success).
func ApplyOwnerAndXattrs(path string, n repo.Node, opts RestoreOptions) []string {
	return applyOwnerAndXattrs(path, n, opts)
}

// Link creates a hard link newname -> oldname, used by the restorer when a
// node's LinkTo points at an already-restored sibling.
func Link(oldname, newname string) error {
	return os.Link(oldname, newname)
}
