//go:build !linux && !darwin && !windows

package fs

import (
	"os"

	"github.com/zephel01/bakku/internal/repo"
)

// collect is a no-op on platforms without a dedicated implementation.
func collect(path string, fi os.FileInfo) (OwnerInfo, bool, map[string][]byte, LinkKey, bool) {
	return OwnerInfo{}, false, nil, LinkKey{}, false
}

// applyOwnerAndXattrs is a no-op on platforms without a dedicated implementation.
func applyOwnerAndXattrs(path string, n repo.Node, opts RestoreOptions) []string {
	return nil
}

// IsRoot always reports false on platforms without a dedicated implementation.
func IsRoot() bool { return false }
