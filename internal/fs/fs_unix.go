//go:build linux || darwin

package fs

import (
	"os"

	"golang.org/x/sys/unix"
)

// xattrDenylist lists attribute name prefixes/exact names that are never
// captured because they are either huge, kernel-managed, or meaningless to
// restore (ACLs are represented as xattrs on some systems but are out of scope
// for Phase 6; we only capture "real" user/system xattrs).
var xattrSkip = map[string]bool{
	// macOS resource fork alias; large and rarely meaningful to restore.
	"com.apple.ResourceFork": true,
}

// listXattrs returns the extended attribute names set on path (symlink-aware:
// operates on the link itself, not its target).
func listXattrs(path string) ([]string, error) {
	// First call with a nil buffer to size it.
	sz, err := unix.Llistxattr(path, nil)
	if err != nil {
		return nil, err
	}
	if sz == 0 {
		return nil, nil
	}
	buf := make([]byte, sz)
	n, err := unix.Llistxattr(path, buf)
	if err != nil {
		return nil, err
	}
	return splitNulTerminated(buf[:n]), nil
}

// splitNulTerminated splits a NUL-separated byte blob (as returned by
// listxattr(2)) into strings.
func splitNulTerminated(b []byte) []string {
	var out []string
	start := 0
	for i, c := range b {
		if c == 0 {
			if i > start {
				out = append(out, string(b[start:i]))
			}
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, string(b[start:]))
	}
	return out
}

// getXattr reads a single named extended attribute from path (symlink-aware).
func getXattr(path, name string) ([]byte, error) {
	sz, err := unix.Lgetxattr(path, name, nil)
	if err != nil {
		return nil, err
	}
	if sz == 0 {
		return []byte{}, nil
	}
	buf := make([]byte, sz)
	n, err := unix.Lgetxattr(path, name, buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

// setXattr writes a single named extended attribute onto path (symlink-aware).
func setXattr(path, name string, value []byte) error {
	return unix.Lsetxattr(path, name, value, 0)
}

// collectXattrs gathers every readable xattr on path, skipping denylisted or
// unreadable ones (best-effort: a single unreadable attribute should not abort
// the whole backup).
func collectXattrs(path string) map[string][]byte {
	names, err := listXattrs(path)
	if err != nil || len(names) == 0 {
		return nil
	}
	out := make(map[string][]byte, len(names))
	for _, name := range names {
		if xattrSkip[name] {
			continue
		}
		val, err := getXattr(path, name)
		if err != nil {
			continue
		}
		out[name] = val
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// chownPath is a thin wrapper around os.Lchown, kept here so callers in this
// package share one place to adjust behavior (e.g. logging) later.
func chownPath(path string, uid, gid int) error {
	return os.Lchown(path, uid, gid)
}
