<div align="center">

# 🗄️ bakku

**バックアップはOSごとにツールがバラバラで壊れやすい。<br>単一バイナリ 1 つで直します。**

[![CI](https://github.com/zephel01/bakku/actions/workflows/ci.yml/badge.svg)](https://github.com/zephel01/bakku/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/zephel01/bakku?color=blue)](https://github.com/zephel01/bakku/releases)
[![Go](https://img.shields.io/badge/go-1.26+-00ADD8?logo=go&logoColor=white)](go.mod)
[![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)](#インストール)
[![License](https://img.shields.io/badge/license-MIT-yellow)](LICENSE)

[English](README.en.md) · **日本語** · [クイックスタート](#-クイックスタート) · [設計詳細](#-設計リポジトリフォーマット)

</div>

---

`bakku` は Go 製のクロスプラットフォーム・バックアップ CLI です。データを内容依存チャンク(keyed FastCDC)に分割し、BLAKE3 ハッシュで重複排除、zstd 圧縮と AES-256-GCM 暗号化を施して、restic 風の content-addressable リポジトリに保存します。ローカルディスクからクラウドまで、6 種類のバックアップ先を同じコマンドで扱えます。

## ✨ 特徴

| | |
|---|---|
| 🔐 **エンドツーエンド暗号化** | 全データ・インデックス・スナップショットを AES-256-GCM で認証付き暗号化。パスワード→argon2id→マスターキーの鍵階層 |
| 🧩 **重複排除 (CAS)** | keyed FastCDC + BLAKE3。同一データはリポジトリ全体で 1 度だけ保存。2 回目以降のバックアップは差分チャンクのみ転送 |
| 🗜️ **zstd 圧縮** | 暗号化前にチャンク単位で圧縮 |
| ☁️ **6 種類のバックアップ先** | ローカル / S3 互換 / SFTP / Google Drive / Dropbox / SMB を URL 1 つで指定 |
| ♻️ **世代管理 (GFS)** | `forget --keep-daily 7 --keep-weekly 4 ...` + `prune` で安全に容量回収 |
| ✅ **検証コマンド** | `check --read-data`(全データ再ハッシュ)と `verify-restore`(サンプル復元テスト) |
| ⏰ **OSネイティブスケジューラ登録** | launchd / systemd timer / タスクスケジューラに 1 コマンドで登録。常駐プロセス不要 |
| 🔔 **Webhook 通知** | Slack / Discord / 汎用 JSON。成功・失敗を個別に設定可 |
| 🖥️ **OS メタデータ保存** | uid/gid・拡張属性・ハードリンク(Linux/macOS)、長パス・共有ロック対応(Windows) |

## 📦 インストール

**リリースバイナリ(推奨)** — [Releases](https://github.com/zephel01/bakku/releases) から自分のプラットフォーム(darwin/linux/windows × amd64/arm64)のアーカイブを取得し、`bakku`(または `bakku.exe`)を `PATH` に置くだけ。

**go install**

```sh
go install github.com/zephel01/bakku/cmd/bakku@latest
```

**ソースからビルド**(Go 1.26+)

```sh
git clone https://github.com/zephel01/bakku
cd bakku && go build -o bakku ./cmd/bakku
```

## 🚀 クイックスタート

```sh
# パスワードを設定(--password-file や対話入力も可)
export BAKKU_PASSWORD='correct horse battery staple'

# リポジトリを作成(ローカル/NASマウント)
bakku init --repo file:///mnt/backups/laptop

# 名前を付けて登録すると、以後は名前で参照できる
bakku dest add laptop file:///mnt/backups/laptop

# バックアップ(増分: 変更チャンクのみ転送)
bakku backup ~/Documents ~/Pictures --repo laptop --tag daily

# スナップショット一覧 / ファイル一覧
bakku snapshots --repo laptop
bakku ls a1b2c3d4 --repo laptop

# 復元
bakku restore a1b2c3d4 --repo laptop --target /tmp/restore
```

## 📖 コマンド一覧

| コマンド | 説明 |
|---|---|
| `bakku init --repo <dest\|URL>` | リポジトリ作成 |
| `bakku backup <paths...> [--tag t] [--exclude glob] [--parallel n]` | 増分バックアップ。新規/再利用チャンク統計を表示 |
| `bakku snapshots` | スナップショット一覧 |
| `bakku restore <snapID> --target <dir> [--include glob] [--chown] [--restore-quarantine]` | 復元 |
| `bakku ls <snapID>` | スナップショット内のファイル一覧 |
| `bakku diff <snapID1> <snapID2>` | 2 スナップショットの差分(追加/削除/変更) |
| `bakku forget --keep-last/daily/weekly/monthly/yearly N [--keep-tag t] [--dry-run] [--prune]` | GFS リテンション適用 |
| `bakku prune [--dry-run]` | 未参照 pack の削除・部分使用 pack の再パックで容量回収 |
| `bakku check [--read-data]` | 整合性検査。`--read-data` で全 blob を再ハッシュ検証 |
| `bakku verify-restore <snapID> [--sample pct]` | ランダムサンプルを一時領域へ復元しハッシュ検証 |
| `bakku dest add/list/remove` | バックアップ先の名前管理 |
| `bakku key add/list/remove` | 鍵スロット管理(複数パスワードで同一リポジトリを開ける) |
| `bakku key add --yubikey` / `--yubikey`(グローバル) | YubiKeyのHMAC-SHA1チャレンジレスポンスでパスワードレス開錠 |
| `bakku password store/forget` | OS キーチェーンへのパスワード保存/削除 |
| `bakku schedule install/uninstall/status` | 定期実行ジョブの管理 |
| `bakku version` | バージョン表示 |

グローバルフラグ: `--repo` / `--config` / `--password-file` / `--password-command` / `--json`(機械可読出力)。`backup`・`prune` は `--no-notify` も受け付けます。

全オプションの意味・デフォルト値・注意点は **[コマンドリファレンス(docs/commands.md)](docs/commands.md)** を、利用シーン別のレシピとトラブル対処は **[事例集(docs/cookbook.md)](docs/cookbook.md)** を参照してください。

### 🔑 鍵スロット(鍵を失っても別の鍵で開ける)

全スロットが同一マスターキーをそれぞれのパスワードでラップするため、どれか 1 つの鍵でリポジトリを開けます。最後の 1 スロットは削除できません。

```sh
# 既存パスワードで開いた上で、新しいパスワードのスロットを追加
bakku key add --repo laptop                 # BAKKU_NEW_PASSWORD / --new-password-file / 対話(2回)
bakku key list --repo laptop                # スロット一覧(ID・type・作成日時・現在使用中 *)
bakku key remove <keyID> --repo laptop      # スロット削除(現在使用中の削除は --force が必要)
```

YubiKeyのHMAC-SHA1チャレンジレスポンスでパスワードレス開錠も可能です(`ykman`/`ykchalresp` が必要。詳細は[クイックガイド](docs/quickguide.md#13-yubikeyでパスワードレス開錠)):

```sh
bakku key add --yubikey --repo laptop       # 既存資格情報で開いた上でYubiKeyスロットを追加
bakku snapshots --repo laptop --yubikey     # パスワードなしでYubiKeyのみで開く
```

> 全スロットをYubiKeyだけにするのは非推奨です(紛失で復元不能)。パスワードスロットを最低1つ残してください。

## ☁️ バックアップ先(URL 形式)

| スキーム | 例 | 認証 |
|---|---|---|
| ローカル/NAS | `file:///mnt/backups` または絶対パス | — |
| S3 互換 | `s3://bucket/prefix?endpoint=...&region=...` | AWS SDK 標準チェーン(`AWS_ACCESS_KEY_ID` 等)。`endpoint` 指定で MinIO/B2/Wasabi/R2 対応 |
| SFTP | `sftp://user@host:port/path` | ssh-agent → `~/.ssh/id_ed25519`/`id_rsa` → `BAKKU_SFTP_PASSWORD`。known_hosts 検証(`BAKKU_SSH_INSECURE=1` でスキップ) |
| Google Drive | `gdrive://folder-path` | `BAKKU_GDRIVE_CREDENTIALS`(client secret JSON)。初回は認可 URL→コード入力、トークンは `~/.config/bakku/gdrive-token.json` に保存 |
| Dropbox | `dropbox://path` | `BAKKU_DROPBOX_TOKEN`。150MB 超は upload session で自動分割 |
| SMB/CIFS | `smb://user@host/share/path` | `BAKKU_SMB_PASSWORD`(NTLMv2)。`DOMAIN\user` 形式可 |

リモートバックエンドは全て指数バックオフ付きリトライ(3 回)と context キャンセルに対応。アップロードは一時名→リネームの擬似アトミック書き込み(SFTP/SMB)。

## ⏰ スケジュール実行

OS ネイティブのスケジューラに登録するため、常駐プロセスも外部 cron も不要です。

```sh
# 毎日 03:00 にバックアップを実行するジョブを登録
bakku schedule install --name daily-docs --cron "0 3 * * *" -- backup ~/Documents --repo laptop

bakku schedule status            # 登録済みジョブ一覧
bakku schedule uninstall --name daily-docs
```

| OS | 登録先 |
|---|---|
| Linux | systemd --user unit + timer(`~/.config/systemd/user/`) |
| macOS | launchd plist(`~/Library/LaunchAgents/com.bakku.<job>.plist`) |
| Windows | タスクスケジューラ(`schtasks.exe`、`Bakku\` フォルダ配下) |

cron 式は標準 5 フィールド。登録コマンドが失敗した場合はファイル生成まで行い、手動登録手順を表示します。

## 🔔 通知

`config.toml` の `[notify]` セクションで設定。`backup` / `prune` 完了時に Webhook へ POST します。

```toml
[notify]
webhook_url = "https://hooks.slack.com/services/T000/B000/XXXX"
on_success = true
on_failure = true
format = "slack"   # "slack" / "discord" / "json"(URL から自動判定)
```

通知の失敗は警告のみで、バックアップ本体の終了コードには影響しません。

## ⚙️ 設定ファイル

`~/.config/bakku/config.toml`(`BAKKU_CONFIG` で上書き可):

```toml
[[dest]]
name = "laptop"
url  = "file:///mnt/backups/laptop"
# password_command = "op read op://Private/bakku-laptop/password"  # dest 固有(任意)

# グローバルのパスワード取得コマンド(dest 固有が無ければこちら)
# password_command = "pass show bakku"
```

パスワードの解決順:
`BAKKU_PASSWORD` → `--password-file` → `--password-command` → config `password_command`(dest 固有 → グローバル) → OS キーチェーン → 対話入力。

外部シークレットマネージャ(1Password / Bitwarden / pass)や OS キーチェーンとの連携例は [docs/quickguide.md](docs/quickguide.md) を参照してください。

## 🏗️ 設計(リポジトリフォーマット)

リポジトリは内容ハッシュでアドレスされる暗号化オブジェクトの集合です:

```
config                 リポジトリ設定 (JSON)
keys/<id>              ラップ済みマスターキー (argon2id + AES-GCM)
data/<xx>/<packID>     pack ファイル (~16 MiB、複数 blob + 暗号化ヘッダ)
index/<indexID>        暗号化インデックス (blob → pack 位置)
snapshots/<snapID>     暗号化スナップショットレコード
```

`backup` のデータフロー: ファイル → keyed FastCDC 分割 → BLAKE3 で重複判定(既存はスキップ) → zstd 圧縮 → AES-256-GCM 暗号化 → pack に集約してアップロード → tree/snapshot 記録。`prune` は全スナップショットからの到達可能性を解析し、「新 pack 書き込み → index 差し替え → 旧 pack 削除」の順序で crash-safe に容量回収します。

チャンク境界の導出に鍵付き FastCDC を用いることで、CDC フィンガープリント攻撃(CCS'25)への耐性を持ちます。パスワードはディスクにもネットワークにも渡りません。

## 🚧 未実装(今後の予定)

Windows VSS(`--use-vss` はスタブ。設計メモは `internal/fs/fs_windows.go`)、Windows 所有者 SID の復元、特殊ファイル(デバイス/FIFO/ソケット)のアーカイブ。

## 🧪 テスト / CI

```sh
go test ./internal/...
```

GitHub Actions(`.github/workflows/ci.yml`)が push/PR ごとに 3 OS でテストし、6 ターゲットをクロスビルド。`vX.Y.Z` タグで `scripts/build-release.sh` によりバイナリを Releases に自動添付します。

## 📄 ライセンス

MIT — [LICENSE](LICENSE) を参照。
