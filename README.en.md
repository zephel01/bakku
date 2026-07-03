<div align="center">

# 🗄️ bakku

**Backup tooling is fragmented and fragile across OSes.<br>One single binary fixes that.**

[![CI](https://github.com/zephel01/bakku/actions/workflows/ci.yml/badge.svg)](https://github.com/zephel01/bakku/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/zephel01/bakku?color=blue)](https://github.com/zephel01/bakku/releases)
[![Go](https://img.shields.io/badge/go-1.26+-00ADD8?logo=go&logoColor=white)](go.mod)
[![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)](#-install)
[![License](https://img.shields.io/badge/license-MIT-yellow)](LICENSE)

**English** · [日本語](README.md) · [Quick start](#-quick-start) · [Design](#%EF%B8%8F-design-repository-format)

</div>

---

`bakku` is a cross-platform, encrypted, deduplicating backup CLI written in Go. Data is split into content-defined chunks (keyed FastCDC), deduplicated by BLAKE3 content hash, zstd-compressed, and AES-256-GCM encrypted before being packed into a restic-like content-addressable repository. Six storage backends — from local disk to cloud — share the same commands.

## ✨ Features

| | |
|---|---|
| 🔐 **End-to-end encryption** | Every data/index/snapshot blob is authenticated-encrypted with AES-256-GCM. Key hierarchy: password → argon2id → wrapped master key |
| 🧩 **Deduplication (CAS)** | Keyed FastCDC + BLAKE3. Identical data is stored once across the whole repository; subsequent backups upload only new chunks |
| 🗜️ **zstd compression** | Chunks are compressed before encryption |
| ☁️ **6 storage backends** | Local / S3-compatible / SFTP / Google Drive / Dropbox / SMB, each addressed by a single URL |
| ♻️ **GFS retention** | `forget --keep-daily 7 --keep-weekly 4 ...` + `prune` reclaims space safely |
| ✅ **Verification** | `check --read-data` (re-hash every blob) and `verify-restore` (sampled restore test) |
| ⏰ **Native scheduling** | One command registers jobs with launchd / systemd timers / Task Scheduler — no daemon required |
| 🔔 **Webhook notifications** | Slack / Discord / generic JSON, with independent success/failure switches |
| 🖥️ **OS metadata** | uid/gid, xattrs, hard links (Linux/macOS); long paths and share-mode-safe reads (Windows) |

## 📦 Install

**Release binaries (recommended)** — grab the archive for your platform (darwin/linux/windows × amd64/arm64) from [Releases](https://github.com/zephel01/bakku/releases) and put `bakku` (or `bakku.exe`) on your `PATH`.

**go install**

```sh
go install github.com/zephel01/bakku/cmd/bakku@latest
```

**Build from source** (Go 1.26+)

```sh
git clone https://github.com/zephel01/bakku
cd bakku && go build -o bakku ./cmd/bakku
```

## 🚀 Quick start

```sh
# Set the repository password (a --password-file or interactive prompt also works)
export BAKKU_PASSWORD='correct horse battery staple'

# Create a repository (local disk or NAS mount)
bakku init --repo file:///mnt/backups/laptop

# Register it under a friendly name
bakku dest add laptop file:///mnt/backups/laptop

# Back up (incremental: only changed chunks are transferred)
bakku backup ~/Documents ~/Pictures --repo laptop --tag daily

# List snapshots / files
bakku snapshots --repo laptop
bakku ls a1b2c3d4 --repo laptop

# Restore
bakku restore a1b2c3d4 --repo laptop --target /tmp/restore
```

## 📖 Commands

| Command | Description |
|---|---|
| `bakku init --repo <dest\|URL>` | Create a new repository |
| `bakku backup <paths...> [--tag t] [--exclude glob] [--parallel n]` | Incremental backup; prints new/reused chunk stats |
| `bakku snapshots` | List snapshots |
| `bakku restore <snapID> --target <dir> [--include glob] [--chown] [--restore-quarantine]` | Restore a snapshot |
| `bakku ls <snapID>` | List files in a snapshot |
| `bakku diff <snapID1> <snapID2>` | Show added/removed/changed paths |
| `bakku forget --keep-last/daily/weekly/monthly/yearly N [--keep-tag t] [--dry-run] [--prune]` | Apply a GFS retention policy |
| `bakku prune [--dry-run]` | Delete unused packs, repack partially-used ones |
| `bakku check [--read-data]` | Verify integrity; `--read-data` re-hashes every blob |
| `bakku verify-restore <snapID> [--sample pct]` | Restore a random sample to a temp dir and verify hashes |
| `bakku dest add/list/remove` | Manage named destinations |
| `bakku key add/list/remove` | Manage key slots (multiple passwords can open one repo) |
| `bakku key add --yubikey` / `--yubikey` (global) | Passwordless unlock via a YubiKey HMAC-SHA1 challenge-response slot |
| `bakku password store/forget` | Save/remove the repository password in the OS keychain |
| `bakku schedule install/uninstall/status` | Manage OS-native scheduled jobs |
| `bakku version` | Print version, commit, build date |

Global flags: `--repo` / `--config` / `--password-file` / `--password-command` / `--json` (machine-readable output). `backup` and `prune` also accept `--no-notify`.

See the **[command reference (docs/commands.md)](docs/commands.md)** for the meaning, defaults, and caveats of every option, and the **[cookbook (docs/cookbook.md)](docs/cookbook.md)** for scenario-based recipes and troubleshooting (both Japanese).

### 🔑 Key slots (lose one key, open with another)

Every slot wraps the same master key with its own password, so any single key
opens the repository. The last remaining slot cannot be removed.

```sh
# Open with an existing credential, then add a new password slot
bakku key add --repo laptop                 # BAKKU_NEW_PASSWORD / --new-password-file / interactive (twice)
bakku key list --repo laptop                # slots (ID, type, created, current *)
bakku key remove <keyID> --repo laptop      # remove a slot (removing the in-use slot needs --force)
```

Passwordless unlock via a YubiKey's HMAC-SHA1 challenge-response is also supported (requires `ykman` or `ykchalresp`; see the [quick guide](docs/quickguide.md#13-yubikeyでパスワードレス開錠), Japanese):

```sh
bakku key add --yubikey --repo laptop       # open with an existing credential, add a YubiKey slot
bakku snapshots --repo laptop --yubikey     # open with only the YubiKey, no password
```

> Making every slot a YubiKey slot is discouraged (losing it makes the repo unrecoverable). Keep at least one password slot.

## ☁️ Storage backends (destination URLs)

| Scheme | Example | Authentication |
|---|---|---|
| Local/NAS | `file:///mnt/backups` or a plain absolute path | — |
| S3-compatible | `s3://bucket/prefix?endpoint=...&region=...` | Standard AWS SDK chain (`AWS_ACCESS_KEY_ID`, etc.). Set `endpoint` for MinIO/B2/Wasabi/R2 |
| SFTP | `sftp://user@host:port/path` | ssh-agent → `~/.ssh/id_ed25519`/`id_rsa` → `BAKKU_SFTP_PASSWORD`; known_hosts verified (`BAKKU_SSH_INSECURE=1` to skip) |
| Google Drive | `gdrive://folder-path` | `BAKKU_GDRIVE_CREDENTIALS` (client secret JSON); first run prints an auth URL, token cached at `~/.config/bakku/gdrive-token.json` |
| Dropbox | `dropbox://path` | `BAKKU_DROPBOX_TOKEN`; files over 150MB use chunked upload sessions automatically |
| SMB/CIFS | `smb://user@host/share/path` | `BAKKU_SMB_PASSWORD` (NTLMv2); `DOMAIN\user` supported |

All remote backends retry transient failures with exponential backoff (3 attempts) and respect context cancellation. SFTP/SMB uploads are pseudo-atomic (temp name + rename).

## ⏰ Scheduling

Jobs are registered with the OS's native scheduler — no long-lived process or external cron needed.

```sh
# Run a backup every day at 03:00
bakku schedule install --name daily-docs --cron "0 3 * * *" -- backup ~/Documents --repo laptop

bakku schedule status            # list bakku-managed jobs
bakku schedule uninstall --name daily-docs
```

| OS | Backend |
|---|---|
| Linux | systemd --user unit + timer (`~/.config/systemd/user/`) |
| macOS | launchd plist (`~/Library/LaunchAgents/com.bakku.<job>.plist`) |
| Windows | Task Scheduler via `schtasks.exe` (under the `Bakku\` folder) |

Cron expressions are standard 5-field. If the scheduler command fails, the generated unit/plist is still written and manual registration steps are printed.

## 🔔 Notifications

Configure the `[notify]` section of `config.toml`; `backup` and `prune` POST a webhook when they finish.

```toml
[notify]
webhook_url = "https://hooks.slack.com/services/T000/B000/XXXX"
on_success = true
on_failure = true
format = "slack"   # "slack" / "discord" / "json" (auto-detected from the URL)
```

Delivery is best-effort: a failed webhook never changes the exit status of `backup`/`prune`.

## ⚙️ Configuration

TOML file at `~/.config/bakku/config.toml` (override with `BAKKU_CONFIG`):

```toml
[[dest]]
name = "laptop"
url  = "file:///mnt/backups/laptop"
# password_command = "op read op://Private/bakku-laptop/password"  # per-dest (optional)

# Global password command (used when a dest has none)
# password_command = "pass show bakku"
```

Password resolution order:
`BAKKU_PASSWORD` → `--password-file` → `--password-command` → config `password_command` (per-dest → global) → OS keychain → interactive prompt.

See [docs/quickguide.md](docs/quickguide.md) for 1Password / Bitwarden / pass and OS-keychain integration examples.

## 🏗️ Design (repository format)

A repository is a flat set of encrypted objects addressed by content hash:

```
config                 repository config (JSON)
keys/<id>              wrapped master key (argon2id + AES-GCM)
data/<xx>/<packID>     pack files (~16 MiB, many blobs + encrypted header)
index/<indexID>        encrypted blob → pack-location index
snapshots/<snapID>     encrypted snapshot records
```

`backup` data flow: file → keyed FastCDC chunking → BLAKE3 dedup lookup (existing blobs skipped) → zstd → AES-256-GCM → packed and uploaded → tree/snapshot records written. `prune` computes reachability from all remaining snapshots and reclaims space crash-safely: new packs are written first, the index is swapped, and only then are old packs deleted.

Keyed FastCDC derives chunk boundaries from a secret key, providing resistance to CDC fingerprinting attacks (CCS'25). The repository password never touches disk or the wire.

## 🚧 Not yet implemented

Windows VSS (`--use-vss` is a stub; design notes in `internal/fs/fs_windows.go`), Windows owner SID restore, special files (devices/FIFOs/sockets).

## 🧪 Testing / CI

```sh
go test ./internal/...
```

GitHub Actions (`.github/workflows/ci.yml`) tests on 3 OSes and cross-builds all 6 targets on every push/PR. Tagging `vX.Y.Z` builds and attaches release binaries via `scripts/build-release.sh`.

## 📄 License

MIT — see [LICENSE](LICENSE).
