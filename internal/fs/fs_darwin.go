//go:build darwin

package fs

import (
	"fmt"
	"os"
	"syscall"

	"github.com/zephel01/bakku/internal/repo"
)

// quarantineXattr is the macOS Gatekeeper quarantine attribute name. By
// default it is excluded from restore (see applyOwnerAndXattrs) so restored
// files are not re-quarantined/blocked from opening; --restore-quarantine
// opts back in.
const quarantineXattr = "com.apple.quarantine"

// collect reads uid/gid, inode/dev, and xattrs for path on macOS.
func collect(path string, fi os.FileInfo) (OwnerInfo, bool, map[string][]byte, LinkKey, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return OwnerInfo{}, false, nil, LinkKey{}, false
	}
	owner := OwnerInfo{UID: st.Uid, GID: st.Gid}
	xattrs := collectXattrs(path)

	var link LinkKey
	hasLink := false
	if fi.Mode().IsRegular() && st.Nlink > 1 {
		link = LinkKey{Dev: uint64(st.Dev), Inode: st.Ino}
		hasLink = true
	}
	return owner, true, xattrs, link, hasLink
}

// applyOwnerAndXattrs restores ownership (if running as root and opts.Chown)
// and xattrs, excluding com.apple.quarantine unless opts.RestoreQuarantine.
func applyOwnerAndXattrs(path string, n repo.Node, opts RestoreOptions) []string {
	var warnings []string
	if opts.Chown && n.OwnerSet {
		if os.Geteuid() == 0 {
			if err := chownPath(path, int(n.UID), int(n.GID)); err != nil {
				warnings = append(warnings, fmt.Sprintf("chown %s: %v", path, err))
			}
		}
	}
	for name, val := range n.Xattrs {
		if name == quarantineXattr && !opts.RestoreQuarantine {
			continue
		}
		if err := setXattr(path, name, val); err != nil {
			warnings = append(warnings, fmt.Sprintf("setxattr %s %s: %v", path, name, err))
		}
	}
	return warnings
}

// IsRoot reports whether the current process is running as root (macOS).
func IsRoot() bool { return os.Geteuid() == 0 }
