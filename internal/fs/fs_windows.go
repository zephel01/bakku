//go:build windows

package fs

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/windows"

	"github.com/zephel01/bakku/internal/repo"
)

// longPathPrefix is prepended to absolute paths to opt out of MAX_PATH (260
// char) limits on Windows, per the \\?\ extended-length path convention.
const longPathPrefix = `\\?\`

// FixPath rewrites an absolute Windows path to its extended-length (\\?\) form
// so syscalls that bypass the usual path-normalization layer (CreateFile,
// etc.) can address paths longer than MAX_PATH. UNC paths become
// \\?\UNC\server\share\..., local paths become \\?\C:\.... Relative paths and
// paths already carrying the prefix are returned unchanged (a relative path
// cannot be usefully long-path-prefixed without first resolving it).
func FixPath(path string) string {
	if path == "" {
		return path
	}
	if strings.HasPrefix(path, longPathPrefix) {
		return path
	}
	if strings.HasPrefix(path, `\\`) {
		// UNC path: \\server\share\... -> \\?\UNC\server\share\...
		return longPathPrefix + `UNC\` + strings.TrimPrefix(path, `\\`)
	}
	if len(path) >= 2 && path[1] == ':' {
		return longPathPrefix + path
	}
	// Not a recognizable absolute path (relative, or already special); leave
	// as-is rather than guessing.
	return path
}

// OpenShared opens path for reading with FILE_SHARE_DELETE included (in
// addition to the usual read/write share flags), matching the behavior of
// tools like VSS-less backup agents that need to read files other processes
// may concurrently rename/delete. Uses the long-path-fixed form of path.
func OpenShared(path string) (*os.File, error) {
	p := FixPath(path)
	pathPtr, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return nil, err
	}
	access := uint32(windows.GENERIC_READ)
	shareMode := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE)
	h, err := windows.CreateFile(
		pathPtr,
		access,
		shareMode,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("fs: CreateFile %s: %w", path, err)
	}
	return os.NewFile(uintptr(h), path), nil
}

// collect reads the owner SID for path on Windows. uid/gid and xattrs have no
// POSIX meaning here, so only OwnerInfo/hasOwner and hasLink are ever
// populated indirectly through the SID path handled by the caller (archiver);
// this function itself returns the SID via a package-level helper since the
// generic Collect() signature is POSIX-shaped. Hard link detection uses the
// file index (volume serial + file index), which is the Windows analogue of
// inode+dev.
func collect(path string, fi os.FileInfo) (OwnerInfo, bool, map[string][]byte, LinkKey, bool) {
	// POSIX ownership/xattrs are not applicable on Windows.
	return OwnerInfo{}, false, nil, LinkKey{}, false
}

// applyOwnerAndXattrs is a no-op on Windows: POSIX chown/xattr restore does
// not apply. Owner SID restoration is not implemented (see OwnerSID field
// comment / README limitations).
func applyOwnerAndXattrs(path string, n repo.Node, opts RestoreOptions) []string {
	return nil
}

// OwnerSID returns the string SID of path's owner, best-effort. Returns "" on
// any failure (insufficient privilege, etc.) rather than erroring, since
// ownership capture is a nice-to-have, not required for a usable backup.
func OwnerSID(path string) string {
	sd, err := windows.GetNamedSecurityInfo(
		FixPath(path),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION,
	)
	if err != nil {
		return ""
	}
	sid, _, err := sd.Owner()
	if err != nil || sid == nil {
		return ""
	}
	return sid.String()
}

// IsElevated reports whether the current process token has administrator
// privileges, used to gate owner-SID restoration attempts.
func IsElevated() bool {
	tok := windows.GetCurrentProcessToken()
	return tok.IsElevated()
}

// IsRoot mirrors the POSIX helper name used by the archiver/restorer for a
// "running with elevated/admin rights" check.
func IsRoot() bool { return IsElevated() }

// --- VSS (Volume Shadow Copy Service) ---
//
// Design notes for a future VSS implementation (not implemented in Phase 6;
// scope explicitly excluded per the Phase 6 plan):
//
//  1. VSS requires COM initialization (CoInitializeEx) and the IVssBackupComponents
//     interface, which is not exposed by golang.org/x/sys/windows; it would need
//     either cgo + vssapi.h, or hand-written COM vtable calls via syscall, or a
//     third-party binding (e.g. github.com/microsoft/... none vendored today).
//  2. A minimal flow would be: InitializeForBackup -> SetBackupState ->
//     GatherWriterMetadata -> StartSnapshotSet -> AddToSnapshotSet(volume) ->
//     PrepareForBackup -> DoSnapshotSet -> resolve the shadow copy device path
//     (\\?\GLOBALROOT\Device\HarddiskVolumeShadowCopyN\...) -> archive files by
//     substituting that prefix for the original volume root -> BackupComplete.
//  3. Error handling must tolerate writers that fail (non-fatal) and always
//     call AbortBackup/BackupComplete to release the shadow copy.
//  4. Because of the required COM/cgo surface, this is deliberately deferred;
//     UseVSS below only validates the flag and warns.
//
// UseVSS validates the --use-vss request and reports that VSS is not yet
// implemented. It always returns an error so callers can decide whether to
// abort or continue without a snapshot.
func UseVSS() error {
	return fmt.Errorf("fs: --use-vss is not implemented yet (Phase 6 scope excludes VSS; see design notes in internal/fs/fs_windows.go)")
}
