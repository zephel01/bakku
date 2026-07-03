//go:build !linux && !darwin && !windows

package scheduler

// Install is not implemented on platforms other than linux/darwin/windows.
func Install(job Job) error { return ErrNotSupported }

// Uninstall is not implemented on platforms other than linux/darwin/windows.
func Uninstall(name string) error { return ErrNotSupported }

// StatusOne is not implemented on platforms other than linux/darwin/windows.
func StatusOne(name string) (Status, error) { return Status{}, ErrNotSupported }

// List is not implemented on platforms other than linux/darwin/windows.
func List() ([]Status, error) { return nil, ErrNotSupported }
