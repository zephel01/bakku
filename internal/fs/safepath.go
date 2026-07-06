package fs

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ValidRestoreName reports whether name is a safe single path element to use
// during restore. It rejects the empty string, "." and "..", and any name
// containing a path separator (OS-native or forward slash) or a NUL byte.
//
// The archiver always stores each node's Name as filepath.Base(path), so a
// legitimate tree only ever contains single-element names. Anything else is a
// crafted or corrupted tree attempting a path-traversal (zip-slip) write, and
// must be refused before the name is joined onto a restore destination.
func ValidRestoreName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.IndexByte(name, 0) >= 0 {
		return false
	}
	if strings.ContainsRune(name, '/') || strings.ContainsRune(name, filepath.Separator) {
		return false
	}
	return true
}

// WithinRoot reports whether path p resolves to a location at or beneath root.
// Both are cleaned before comparison. It is a defense-in-depth companion to
// ValidRestoreName: even if name validation were bypassed, a write is only
// permitted when its destination stays inside the restore root.
func WithinRoot(root, p string) bool {
	root = filepath.Clean(root)
	p = filepath.Clean(p)
	if p == root {
		return true
	}
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	// filepath.Rel can return an absolute path when root and p are on
	// different volumes (Windows); such a path never stays within root.
	if filepath.IsAbs(rel) {
		return false
	}
	return true
}

// SafeJoin validates name as a single safe element, joins it under dir, and
// confirms the result stays within root. It returns an error suitable for
// aborting a restore when a crafted/corrupted tree tries to escape the target.
func SafeJoin(root, dir, name string) (string, error) {
	if !ValidRestoreName(name) {
		return "", fmt.Errorf("unsafe entry name %q", name)
	}
	dst := filepath.Join(dir, name)
	if !WithinRoot(root, dst) {
		return "", fmt.Errorf("entry %q escapes restore root", name)
	}
	return dst, nil
}
