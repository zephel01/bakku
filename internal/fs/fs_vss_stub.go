//go:build !windows

package fs

import "fmt"

// UseVSS reports that VSS is a Windows-only concept; on other platforms
// --use-vss is simply not applicable.
func UseVSS() error {
	return fmt.Errorf("fs: --use-vss is only meaningful on Windows")
}
