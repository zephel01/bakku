// Package keyguard validates storage keys at the backend boundary. Repository
// keys are internally generated ("data/ab/cd", "keys/<id>", "config",
// "index/<id>"), but validating them defensively where they enter a backend
// prevents a future bug or a tampered value from escaping the repository root
// (or share subtree) via ".." path elements or an absolute path.
//
// It is a leaf package (no dependency on the parent backend package) so every
// backend can import it without an import cycle.
package keyguard

import (
	"fmt"
	"strings"
)

// Validate reports an error if key is unsafe to use as a storage key or list
// prefix. An empty key is allowed (callers that require a non-empty key fail
// naturally on their own). A key is rejected when it is absolute (leading '/'),
// contains a NUL byte, or contains a "." or ".." path element (which could
// collapse to escape the intended root when joined).
func Validate(key string) error {
	if key == "" {
		return nil
	}
	if strings.IndexByte(key, 0) >= 0 {
		return fmt.Errorf("keyguard: key %q contains NUL", key)
	}
	if strings.HasPrefix(key, "/") {
		return fmt.Errorf("keyguard: absolute key %q not allowed", key)
	}
	// Backends use '/' as the key separator; also reject backslash to be safe
	// on filesystems that treat it as a separator.
	norm := strings.ReplaceAll(key, "\\", "/")
	for _, elem := range strings.Split(norm, "/") {
		if elem == ".." || elem == "." {
			return fmt.Errorf("keyguard: key %q contains %q path element", key, elem)
		}
	}
	return nil
}
