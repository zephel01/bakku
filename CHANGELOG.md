# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/zephel01/bakku/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/zephel01/bakku/releases/tag/v0.1.0
