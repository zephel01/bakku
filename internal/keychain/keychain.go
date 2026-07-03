// Package keychain stores and retrieves bakku repository passwords in the OS
// secret store (macOS Keychain via the `security` command, Windows Credential
// Manager via wincred syscalls, Linux Secret Service via D-Bus). It uses
// github.com/zalando/go-keyring, which is pure Go (no cgo) on all platforms, so
// bakku still cross-compiles with CGO_ENABLED=0 for every target.
//
// On headless systems where no secret service is reachable (e.g. a Linux box
// without a running D-Bus / gnome-keyring), Get returns ErrUnavailable and
// callers should silently fall through to the next password source.
package keychain

import (
	"errors"
	"strings"

	"github.com/zalando/go-keyring"
)

// service is the fixed service name under which bakku stores passwords.
const service = "bakku"

// ErrNotFound indicates there is no stored password for the given repository.
var ErrNotFound = errors.New("keychain: no stored password for repository")

// ErrUnavailable indicates the OS secret store could not be reached (e.g. no
// D-Bus session on a headless Linux host). Callers should treat this as "no
// password here" and fall through to the next source rather than failing.
var ErrUnavailable = errors.New("keychain: OS secret store unavailable")

// normalizeKey derives the storage key from a repository destination. It is the
// repo/dest string with surrounding whitespace trimmed and a trailing slash
// removed so that "file:///backups/x" and "file:///backups/x/" collide. We keep
// it deliberately simple (no scheme rewriting) so `password store` and later
// lookups agree as long as the same --repo value is used.
func normalizeKey(repo string) string {
	k := strings.TrimSpace(repo)
	k = strings.TrimRight(k, "/")
	return k
}

// Set stores password for repo in the OS secret store.
func Set(repo string, password []byte) error {
	if err := keyring.Set(service, normalizeKey(repo), string(password)); err != nil {
		return classify(err)
	}
	return nil
}

// Get retrieves the stored password for repo. It returns ErrNotFound if no
// entry exists, or ErrUnavailable if the secret store is unreachable.
func Get(repo string) ([]byte, error) {
	pw, err := keyring.Get(service, normalizeKey(repo))
	if err != nil {
		return nil, classify(err)
	}
	return []byte(pw), nil
}

// Delete removes the stored password for repo. Deleting a missing entry returns
// ErrNotFound.
func Delete(repo string) error {
	if err := keyring.Delete(service, normalizeKey(repo)); err != nil {
		return classify(err)
	}
	return nil
}

// classify maps go-keyring errors to our sentinel errors. Anything that is not
// a clean "not found" is treated as the store being unavailable, so callers can
// fall through gracefully on headless/misconfigured systems.
func classify(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, keyring.ErrNotFound) {
		return ErrNotFound
	}
	return ErrUnavailable
}
