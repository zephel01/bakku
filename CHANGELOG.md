# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.4] - 2026-07-06

### Security

- **YubiKey KEK now derived with HKDF-SHA256.** New `yubikey-chalresp` key slots
  stretch the challenge-response into a KEK using standard HKDF (salt as the
  HKDF salt, fixed context as info) instead of the previous BLAKE3
  concatenation. **Backward compatible:** slots created by earlier versions are
  marked/detected and keep unlocking via the legacy derivation, so existing
  YubiKey-protected repositories open unchanged.
- **In-memory key material is now wiped after use.** Password-derived KEKs and
  YubiKey KEKs are zeroed immediately after wrapping/unwrapping the master key,
  and the master key plus derived subkeys are zeroed on `Repository.Close`
  (best-effort defense in depth; see `crypto.Wipe`).
- **Backend storage keys are validated** to reject `..` path elements and
  absolute paths at every backend entry point (local, SFTP, SMB, S3, Google
  Drive, Dropbox), a defense-in-depth guard against traversal outside the
  repository root / share subtree.
- **SFTP prints a warning** to stderr when `BAKKU_SSH_INSECURE=1` disables host
  key verification, so an accidentally-set insecure mode is visible.

### Added

- `docs/security.md`: security notes covering the cryptography overview,
  AES-GCM nonce limits and rotation guidance for very large/long-lived
  repositories, security-relevant environment variables, on-disk protection,
  and restore safety.

### Notes

- No repository-format break: existing repositories (including YubiKey slots)
  open unchanged. Newly created YubiKey slots use HKDF and cannot be opened by
  bakku versions older than 0.2.4.

## [0.2.1] - 2026-07-03

### Fixed

- `bakku version` / `--version` printed `dev (commit none, built unknown)` for
  binaries built without release ldflags. Version metadata now falls back to
  the Go toolchain's embedded build info: `go install .../cmd/bakku@vX.Y.Z`
  reports the module version, and a plain `go build` inside a git checkout
  reports the commit/date (with a `-dirty` suffix for modified worktrees).
  ldflags-injected values still take precedence, and unknown fields are
  omitted from the output.

## [0.2.0] - 2026-07-03

### Added

- Multiple key slots per repository: `key add` / `key list` / `key remove`
  (all `--json`). Every slot wraps the same master key with its own password,
  so losing one key still lets you open the repository with another. The last
  remaining slot cannot be removed. Key files gained a `type` field; v0.1.0
  key files (no `type`) are read as `password` slots (backward compatible).
- Flexible password resolution: `--password-command` global flag and
  `password_command` config key (global and per-`[[dest]]`) run an external
  command (1Password `op`, Bitwarden `bw`, `pass`, …) whose first stdout line
  is the password.
- OS keychain integration via `github.com/zalando/go-keyring` (cgo-free):
  `password store` / `password forget` save and remove the repository password
  in the macOS Keychain, Windows Credential Manager, or Linux Secret Service.
  Missing entries or a headless host silently fall through to the next source.
- Password resolution order is now: `BAKKU_PASSWORD` → `--password-file` →
  `--password-command` → config `password_command` (per-dest → global) → OS
  keychain → interactive prompt.
- YubiKey challenge-response key slots (`yubikey-chalresp`): `key add
  --yubikey [--yubikey-slot N]` registers a passwordless slot using the
  YubiKey's HMAC-SHA1 OTP challenge-response (same scheme as KeePassXC),
  driven through `ykchalresp` (yubikey-personalization) or `ykman`
  (yubikey-manager) — no cgo, no new Go dependency. The global `--yubikey`
  flag unlocks with a registered YubiKey instead of a password; without the
  flag, bakku auto-falls-back to YubiKey unlock if password resolution fails
  and a YubiKey tool/slot is available. `key list` shows the slot type; `key
  add --yubikey` and `key remove` warn (and `key remove` requires `--force`)
  when an operation would leave the repository with no password slot at all.
  See docs/quickguide.md § 13 for setup.
- Documentation: full command reference (docs/commands.md) covering every
  command, flag meaning/default, password resolution order, and environment
  variables; user quick guide (docs/quickguide.md); English README
  (README.en.md).

### Fixed

- CLI e2e tests built repository URLs as `"file://" + path`, which produced
  an invalid URL on Windows (`file://C:\...`) and failed the windows-latest
  CI job; tests now use the plain-path repo spec.

## [0.1.0] - 2026-07-03

Initial release.

### Added

- Content-addressable repository format: keyed FastCDC chunking, BLAKE3
  content addressing, zstd compression, AES-256-GCM encryption with an
  argon2id-wrapped master key (repository format version 1).
- Core commands: `init`, `backup` (incremental, dedup), `restore`,
  `snapshots`, `ls`, `diff`.
- Retention & maintenance: `forget` (GFS keep-last/daily/weekly/monthly/
  yearly/tag), `prune` (crash-safe repack + reclaim), `check
  [--read-data]`, `verify-restore`.
- Storage backends: local/NAS (`file://`), S3-compatible (`s3://`), SFTP
  (`sftp://`), Google Drive (`gdrive://`), Dropbox (`dropbox://`), SMB
  (`smb://`), all with retry/backoff.
- Destination management (`dest add/list/remove`) and TOML configuration.
- OS metadata: uid/gid, xattrs, hard links (Linux/macOS); long paths and
  `FILE_SHARE_DELETE` reads (Windows).
- Scheduling: `schedule install/uninstall/status` via systemd timers,
  launchd, or Windows Task Scheduler.
- Webhook notifications (Slack / Discord / JSON) for `backup` and `prune`.
- CI (3-OS test matrix, 6-target cross-build) and release automation
  (`scripts/build-release.sh`, binaries attached on `v*` tags).

[Unreleased]: https://github.com/zephel01/bakku/compare/v0.2.4...HEAD
[0.2.4]: https://github.com/zephel01/bakku/compare/v0.2.3...v0.2.4
[0.2.1]: https://github.com/zephel01/bakku/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/zephel01/bakku/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/zephel01/bakku/releases/tag/v0.1.0
