//go:build linux

package fs

import (
	"fmt"
	"os"
	"syscall"

	"github.com/zephel01/bakku/internal/repo"
)

// collect reads uid/gid, inode/dev, and xattrs for path on Linux.
func collect(path string, fi os.FileInfo) (OwnerInfo, bool, map[string][]byte, LinkKey, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return OwnerInfo{}, false, nil, LinkKey{}, false
	}
	owner := OwnerInfo{UID: st.Uid, GID: st.Gid}
	xattrs := collectXattrs(path)

	var link LinkKey
	hasLink := false
	// Hard-link detection only makes sense for regular files with more than one
	// link; directories cannot be hard-linked on Linux, and symlinks each have
	// their own inode.
	if fi.Mode().IsRegular() && st.Nlink > 1 {
		link = LinkKey{Dev: uint64(st.Dev), Inode: st.Ino}
		hasLink = true
	}
	return owner, true, xattrs, link, hasLink
}

// applyOwnerAndXattrs restores ownership (if running as root and opts.Chown)
// and xattrs (skipping nothing on Linux; the quarantine xattr is macOS-only).
func applyOwnerAndXattrs(path string, n repo.Node, opts RestoreOptions) []string {
	var warnings []string
	if opts.Chown && n.OwnerSet {
		if os.Geteuid() == 0 {
			if err := chownPath(path, int(n.UID), int(n.GID)); err != nil {
				warnings = append(warnings, fmt.Sprintf("chown %s: %v", path, err))
			}
		}
		// Non-root: silently skip (restoring ownership requires privileges on
		// Linux); this is expected, not a warning-worthy condition.
	}
	for name, val := range n.Xattrs {
		if err := setXattr(path, name, val); err != nil {
			warnings = append(warnings, fmt.Sprintf("setxattr %s %s: %v", path, name, err))
		}
	}
	return warnings
}

// IsRoot reports whether the current process is running as root (Linux).
func IsRoot() bool { return os.Geteuid() == 0 }
