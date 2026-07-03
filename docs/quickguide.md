# bakku クイックガイド — コピペで始めるバックアップ

そのままコピペで使える手順集です。`<>` で囲まれた部分だけ自分の環境に置き換えてください。

> 前提: [README](../README.md#-インストール) の手順で `bakku` をインストール済みであること。
> 確認: `bakku version`

---

## 目次

1. [最初に1回だけやること](#1-最初に1回だけやること)
2. [外付けドライブ/ローカルにバックアップ](#2-外付けドライブローカルにバックアップ)
3. [NASにバックアップ (SMB)](#3-nasにバックアップ-smb)
4. [別のPCにバックアップ (SFTP)](#4-別のpcにバックアップ-sftp)
5. [クラウドにバックアップ (S3互換: B2/Wasabi/R2/MinIO)](#5-クラウドにバックアップ-s3互換)
6. [Google Driveにバックアップ](#6-google-driveにバックアップ)
7. [Dropboxにバックアップ](#7-dropboxにバックアップ)
8. [毎日自動でバックアップする](#8-毎日自動でバックアップする)
9. [復元する](#9-復元する)
10. [古いバックアップの整理と検証(月1回の習慣)](#10-古いバックアップの整理と検証)
11. [Slack/Discordに結果を通知する](#11-slackdiscordに結果を通知する)
12. [1Password/キーチェーン連携(パスワードを自動取得)](#12-1passwordキーチェーン連携)
13. [YubiKeyでパスワードレス開錠](#13-yubikeyでパスワードレス開錠)
14. [トラブルシューティング](#14-トラブルシューティング)

---

## 1. 最初に1回だけやること

### パスワードを決めて保存する

リポジトリのパスワードは**全データの暗号鍵**です。忘れると復元できません。パスワードマネージャ等に必ず控えてください。

毎回入力を省くには、パスワードファイルを作っておくのが簡単です:

**macOS / Linux**

```sh
mkdir -p ~/.config/bakku
echo '<あなたのパスワード>' > ~/.config/bakku/password
chmod 600 ~/.config/bakku/password
```

**Windows (PowerShell)**

```powershell
mkdir "$env:USERPROFILE\.config\bakku" -Force
Set-Content "$env:USERPROFILE\.config\bakku\password" '<あなたのパスワード>'
```

以後の全コマンドに `--password-file ~/.config/bakku/password` を付けるか、環境変数で渡します:

```sh
export BAKKU_PASSWORD='<あなたのパスワード>'   # このシェルセッション内のみ有効
```

以下の手順は `BAKKU_PASSWORD` 設定済みの前提で書きます。

---

## 2. 外付けドライブ/ローカルにバックアップ

### 初期設定(1回だけ)

**macOS**(外付けドライブ名が `MyBackupHDD` の場合)

```sh
bakku init --repo /Volumes/MyBackupHDD/bakku-repo
bakku dest add usb /Volumes/MyBackupHDD/bakku-repo
```

**Linux**

```sh
bakku init --repo /mnt/backup/bakku-repo
bakku dest add usb /mnt/backup/bakku-repo
```

**Windows (PowerShell)**(外付けが `E:` の場合)

```powershell
bakku init --repo E:\bakku-repo
bakku dest add usb E:\bakku-repo
```

### バックアップ実行(2回目以降は変更分のみで高速)

```sh
# macOS の例: 書類・写真・デスクトップ
bakku backup ~/Documents ~/Pictures ~/Desktop --repo usb --tag daily

# 除外パターン(node_modules とキャッシュを除く)
bakku backup ~/projects --repo usb --exclude '**/node_modules/**' --exclude '**/.cache/**'
```

```powershell
# Windows の例
bakku backup $env:USERPROFILE\Documents $env:USERPROFILE\Pictures --repo usb --tag daily
```

### 確認

```sh
bakku snapshots --repo usb
```

---

## 3. NASにバックアップ (SMB)

**方法A: OSでマウント済みのNASをローカル扱いする(簡単・推奨)**

```sh
# macOS: Finderで接続済み(smb://nas.local/backup)なら
bakku init --repo /Volumes/backup/bakku-repo
bakku dest add nas /Volumes/backup/bakku-repo

# Linux: /etc/fstab 等で /mnt/nas にマウント済みなら
bakku init --repo /mnt/nas/bakku-repo
bakku dest add nas /mnt/nas/bakku-repo
```

```powershell
# Windows: ネットワークドライブ Z: に割当済みなら
bakku init --repo Z:\bakku-repo
bakku dest add nas Z:\bakku-repo
```

**方法B: bakkuが直接SMB接続する(マウント不要)**

```sh
export BAKKU_SMB_PASSWORD='<NASのパスワード>'
bakku init --repo 'smb://<ユーザー名>@<NASのIPまたはホスト名>/<共有名>/bakku-repo'
bakku dest add nas 'smb://<ユーザー名>@<NASのIPまたはホスト名>/<共有名>/bakku-repo'
```

実行はどちらの方法でも同じ:

```sh
bakku backup ~/Documents --repo nas --tag daily
```

> 方法Bは実行のたびに `BAKKU_SMB_PASSWORD` の設定が必要です(スケジュール実行では後述のユニット/タスクに環境変数を含めます)。

---

## 4. 別のPCにバックアップ (SFTP)

バックアップ先PCにSSHで入れることが前提です(`ssh <ユーザー>@<ホスト>` が通ること)。

### 鍵認証の準備(未設定の場合のみ)

```sh
ssh-keygen -t ed25519            # 質問はすべてEnterでOK
ssh-copy-id <ユーザー>@<バックアップ先ホスト>
```

### 初期設定(1回だけ)

```sh
bakku init --repo 'sftp://<ユーザー>@<バックアップ先ホスト>/home/<ユーザー>/bakku-repo'
bakku dest add remote-pc 'sftp://<ユーザー>@<バックアップ先ホスト>/home/<ユーザー>/bakku-repo'
```

ポートが22以外の場合: `sftp://<ユーザー>@<ホスト>:2222/path`

### 実行

```sh
bakku backup ~/Documents ~/projects --repo remote-pc --tag daily
```

> 認証は ssh-agent → `~/.ssh/id_ed25519`/`id_rsa` → 環境変数 `BAKKU_SFTP_PASSWORD` の順で試行されます。ホスト鍵は `~/.ssh/known_hosts` で検証されます(初回は一度 `ssh` で接続して登録しておくとスムーズ)。

---

## 5. クラウドにバックアップ (S3互換)

Backblaze B2 / Wasabi / Cloudflare R2 / MinIO / AWS S3 すべて同じ手順です。

### 認証情報を設定

```sh
export AWS_ACCESS_KEY_ID='<アクセスキーID>'
export AWS_SECRET_ACCESS_KEY='<シークレットキー>'
```

### 初期設定(1回だけ) — サービス別のURL例

```sh
# AWS S3
bakku init --repo 's3://<バケット名>/bakku?region=ap-northeast-1'

# Backblaze B2
bakku init --repo 's3://<バケット名>/bakku?endpoint=https://s3.us-west-004.backblazeb2.com&region=us-west-004'

# Wasabi
bakku init --repo 's3://<バケット名>/bakku?endpoint=https://s3.ap-northeast-1.wasabisys.com&region=ap-northeast-1'

# Cloudflare R2
bakku init --repo 's3://<バケット名>/bakku?endpoint=https://<アカウントID>.r2.cloudflarestorage.com&region=auto'

# 自宅MinIO
bakku init --repo 's3://<バケット名>/bakku?endpoint=http://192.168.1.10:9000&region=us-east-1'
```

続けて名前を登録(URLは init と同じものを指定):

```sh
bakku dest add cloud 's3://<バケット名>/bakku?endpoint=...&region=...'
```

### 実行

```sh
bakku backup ~/Documents --repo cloud --tag daily
```

> 転送されるのは圧縮+暗号化済みの差分チャンクのみなので、2回目以降の通信量はわずかです。

---

## 6. Google Driveにバックアップ

### 準備(1回だけ)

1. [Google Cloud Console](https://console.cloud.google.com/) でプロジェクトを作成し、**Google Drive API を有効化**
2. 「認証情報」→「OAuthクライアントID」→ 種類は**デスクトップアプリ** → JSONをダウンロード
3. JSONのパスを環境変数に設定:

```sh
export BAKKU_GDRIVE_CREDENTIALS=~/Downloads/client_secret_XXXX.json
```

### 初期設定(1回だけ)

```sh
bakku init --repo gdrive://bakku-backup
# → 表示されるURLをブラウザで開いてログイン→コードをターミナルに貼り付け
bakku dest add gdrive gdrive://bakku-backup
```

トークンは `~/.config/bakku/gdrive-token.json` に保存され、次回から自動でログインされます。

### 実行

```sh
bakku backup ~/Documents --repo gdrive --tag daily
```

---

## 7. Dropboxにバックアップ

### 準備(1回だけ)

1. [Dropbox App Console](https://www.dropbox.com/developers/apps) でアプリを作成(Scoped access / Full Dropbox または App folder)
2. Permissionsタブで `files.content.write` と `files.content.read` を有効化
3. 「Generate access token」でトークンを発行

```sh
export BAKKU_DROPBOX_TOKEN='<発行したトークン>'
```

### 初期設定と実行

```sh
bakku init --repo dropbox://bakku-backup
bakku dest add dropbox dropbox://bakku-backup
bakku backup ~/Documents --repo dropbox --tag daily
```

---

## 8. 毎日自動でバックアップする

毎日 03:00 に実行する例です(`usb` の部分は自分の dest 名に置き換え)。

```sh
bakku schedule install --name daily-backup --cron "0 3 * * *" -- backup ~/Documents ~/Pictures --repo usb --tag daily --password-file ~/.config/bakku/password
```

> スケジュール実行には環境変数が渡らないため、**必ず `--password-file` を含めてください**(SMB/SFTPパスワード等が必要な場合は方法Aのマウント方式か鍵認証を推奨)。

よく使うcron式:

| 実行タイミング | cron式 |
|---|---|
| 毎日 03:00 | `0 3 * * *` |
| 平日 12:30 | `30 12 * * 1-5` |
| 毎週日曜 02:00 | `0 2 * * 0` |
| 6時間ごと | `0 */6 * * *` |

管理コマンド:

```sh
bakku schedule status                          # 登録済みジョブ一覧
bakku schedule uninstall --name daily-backup   # 解除
```

登録先: macOS=launchd / Linux=systemd user timer / Windows=タスクスケジューラ。PCがスリープ中は実行されないため、ノートPCでは起動している時間帯を指定してください。

---

## 9. 復元する

### スナップショットを探す

```sh
bakku snapshots --repo usb
# ID        TIME                 HOST    TAGS   PATHS
# a1b2c3d4  2026-07-03 03:00:12  mymac   daily  /Users/me/Documents
```

### 中身を確認してから復元

```sh
# ファイル一覧
bakku ls a1b2c3d4 --repo usb

# 前回バックアップとの差分を確認
bakku diff <古いID> <新しいID> --repo usb
```

### 全体を復元

```sh
bakku restore a1b2c3d4 --repo usb --target ~/restored
```

> 復元先(`--target`)は**空のディレクトリか新規パス**を指定してください。元の場所に直接上書きせず、いったん別の場所に復元して確認してから移動するのが安全です。

### 特定のファイル/フォルダだけ復元

```sh
# 例: report.xlsx だけ
bakku restore a1b2c3d4 --repo usb --target ~/restored --include '**/report.xlsx'

# 例: 写真フォルダ以下だけ
bakku restore a1b2c3d4 --repo usb --target ~/restored --include 'Pictures/**'
```

### 所有者情報も復元する(サーバー用途、root実行時のみ有効)

```sh
sudo -E bakku restore a1b2c3d4 --repo usb --target /restored --chown
```

---

## 10. 古いバックアップの整理と検証

月に1回程度、以下をまとめて実行するのがおすすめです。

### 世代整理(直近7日+4週+6ヶ月分を残す)

```sh
# まずドライラン(何が消えるか確認だけ)
bakku forget --repo usb --keep-daily 7 --keep-weekly 4 --keep-monthly 6 --dry-run

# 問題なければ実行+容量回収
bakku forget --repo usb --keep-daily 7 --keep-weekly 4 --keep-monthly 6 --prune
```

### 整合性チェック

```sh
# 構造チェック(高速)
bakku check --repo usb

# 全データ再ハッシュ検証(時間がかかるが確実)
bakku check --repo usb --read-data
```

### 復元テスト(「復元できないバックアップ」を防ぐ)

```sh
# ランダムに10%のファイルを一時領域へ実際に復元して検証
bakku verify-restore <スナップショットID> --repo usb
```

これも自動化できます:

```sh
bakku schedule install --name weekly-check --cron "0 4 * * 0" -- check --repo usb --read-data --password-file ~/.config/bakku/password
```

---

## 11. Slack/Discordに結果を通知する

`~/.config/bakku/config.toml` に追記:

```toml
[notify]
webhook_url = "https://hooks.slack.com/services/T000/B000/XXXX"  # DiscordのWebhook URLも可
on_success = true    # 成功時も通知(失敗時だけでよければ false)
on_failure = true
```

以後、`backup` / `prune` の完了時に自動通知されます。1回だけ通知を止めたいときは `--no-notify` を付けます。

---

## 12. 1Password/キーチェーン連携

パスワードファイルを平文で置く代わりに、外部のシークレットマネージャや OS のキーチェーンから自動取得できます。

**パスワードの解決順**(先に見つかったものを使用):
`BAKKU_PASSWORD` → `--password-file` → `--password-command` → config の `password_command`(dest 固有 → グローバル) → OS キーチェーン → 対話入力。

### 方法A: 外部コマンドから取得(`--password-command` / `password_command`)

コマンドの**標準出力の1行目**(末尾改行は除去)がパスワードになります。終了コードが 0 以外ならエラーです。

```sh
# 1Password CLI
bakku backup ~/Documents --repo laptop \
  --password-command "op read op://Private/bakku-laptop/password"

# Bitwarden CLI(事前に bw unlock 済みで BW_SESSION 設定)
bakku backup ~/Documents --repo laptop \
  --password-command "bw get password bakku-laptop"

# pass (password-store)
bakku backup ~/Documents --repo laptop \
  --password-command "pass show bakku/laptop"
```

毎回書くのが面倒なら `config.toml` に登録します(dest 固有・グローバルの両方に置けます):

```toml
# グローバル(全 dest 共通のフォールバック)
password_command = "pass show bakku"

[[dest]]
name = "laptop"
url  = "file:///mnt/backups/laptop"
password_command = "op read op://Private/bakku-laptop/password"  # この dest だけ 1Password
```

### 方法B: OS キーチェーンに保存(`password store`)

macOS キーチェーン / Windows 資格情報マネージャー / Linux Secret Service に保存しておくと、以後はコマンドもファイルも不要で開けます。

```sh
# 保存(対話やファイル/コマンドで取得したパスワードをキーチェーンへ)
export BAKKU_PASSWORD='<あなたのパスワード>'
bakku password store --repo laptop
unset BAKKU_PASSWORD

# 以後はそのまま開ける(キーチェーンから自動取得)
bakku snapshots --repo laptop

# 保存を取り消す
bakku password forget --repo laptop
```

> ヘッドレスな Linux(D-Bus/Secret Service が無い環境)では保存に失敗することがあります。その場合はキーチェーンをスキップして次の手段(対話入力など)に自動フォールバックします。方法A の `--password-command` を使ってください。

---

## 13. YubiKeyでパスワードレス開錠

YubiKey の HMAC-SHA1 チャレンジレスポンス機能(KeePassXC などと同じ方式)を使い、パスワード入力なしでリポジトリを開けます。物理キーを持っている人しか開けないので、パスワードより強固かつ手軽です。

### 前提: ツールのインストールとYubiKeyの設定(1回だけ)

どちらか一方でOKです(両方入っている場合は `ykchalresp` が優先されます)。

**方法A: yubikey-manager (`ykman`)**

```sh
# macOS
brew install ykman

# Linux (Debian/Ubuntu)
sudo apt install yubikey-manager

# Windows
winget install Yubico.YubikeyManager
```

**方法B: yubikey-personalization (`ykchalresp`)**

```sh
# macOS
brew install ykpers

# Linux (Debian/Ubuntu)
sudo apt install yubikey-personalization
```

YubiKey の **スロット2** に HMAC-SHA1 チャレンジレスポンスを設定します(スロット1は工場出荷時のYubico OTPのままにしておくのが無難です)。

```sh
ykman otp chalresp --generate 2
```

> `--generate` はランダムなHMAC秘密鍵をYubiKey内部に書き込みます(取り出し不可)。既にスロット2を他用途で使っている場合は上書きされるため注意してください。別スロットを使う場合は `bakku key add --yubikey-slot <番号>` で指定できます。

### YubiKeyスロットを追加する

既存のパスワードで一度リポジトリを開いた上で、YubiKeyスロットを追加します(2回チャレンジを送って応答が安定していることを自動確認してから保存されます。タッチを2回求められます)。

```sh
BAKKU_PASSWORD='<既存のパスワード>' bakku key add --yubikey --repo laptop
# YubiKeyにタッチしてください... (1回目)
# YubiKeyにタッチしてください... (2回目)
# added yubikey key slot ab12cd34ef56
```

確認:

```sh
bakku key list --repo laptop --yubikey
# ID            TYPE              CREATED               CURRENT
# 1a2b3c4d5e6f  password          2026-07-03T10:00:00+09:00
# ab12cd34ef56  yubikey-chalresp  2026-07-03T10:05:00+09:00  *
```

### 以後、パスワードレスで開く

```sh
bakku snapshots --repo laptop --yubikey
# YubiKeyにタッチしてください...
```

`--yubikey` を付けなくても、`BAKKU_PASSWORD` 等のパスワード解決手段が全て失敗し、かつリポジトリにYubiKeyスロットがあり、かつ `ykchalresp`/`ykman` がPATH上にある場合は自動でYubiKey開錠にフォールバックします(スケジュール実行などパスワードを渡していない場合に有用です)。

### バックアップ用に2本目のYubiKeyを登録する(強く推奨)

YubiKeyを1本失くすとそのスロットは二度と使えません。**必ず2本目のYubiKeyも登録**し、金庫等に保管してください。

```sh
# 2本目のYubiKeyをスロット2にも同じくHMAC-SHA1で設定してから挿し替えて実行
BAKKU_PASSWORD='<既存のパスワード>' bakku key add --yubikey --repo laptop
```

### パスワードスロットの併存を推奨(保険)

**全スロットをYubiKeyだけにするのは推奨しません。** YubiKeyを2本とも紛失・破損すると、リポジトリは永久に復元不能になります。少なくとも1つはパスワードスロットを残してください。

- `key add --yubikey` でリポジトリ内が全てYubiKeyスロットになる状態を作ると、警告が表示されます(操作自体は可能)。
- `key remove` で最後のパスワードスロットを削除しようとした場合(YubiKeyのみが残る)、`--force` を付けない限り拒否されます。

```sh
# 誤ってパスワードスロットを全部消してしまわないためのガード。強行する場合のみ:
bakku key remove <passwordのkeyID> --repo laptop --force
```

---

## 14. トラブルシューティング

| 症状 | 対処 |
|---|---|
| `wrong password` | パスワードが違います。`BAKKU_PASSWORD` と `--password-file` の両方を確認(環境変数が優先) |
| macOSで `operation not permitted` | システム設定 → プライバシーとセキュリティ → **フルディスクアクセス** にターミナル(またはbakku)を追加 |
| SFTPで `knownhosts: key is unknown` | 一度 `ssh <ユーザー>@<ホスト>` で接続してホスト鍵を登録。検証を省略する場合のみ `BAKKU_SSH_INSECURE=1`(非推奨) |
| S3互換で checksum 系エラー | bakkuは対策済みですが、endpoint/regionの指定ミスが多いのでURLを再確認 |
| スケジュールが動かない | `bakku schedule status` で登録確認。パスワードを `--password-file` で渡しているか確認。PCがその時刻に起動しているか確認 |
| Google Driveで認証エラー | `~/.config/bakku/gdrive-token.json` を削除して再認証 |
| バックアップが遅い | `--parallel <数>` で並列度を調整。初回はフルアップロードのため時間がかかります(2回目以降は差分のみ) |
| リポジトリの容量が減らない | スナップショット削除(`forget`)だけでは領域は戻りません。`--prune` を付けるか別途 `bakku prune` を実行 |
| `no YubiKey tool found on PATH` | `ykman` または `ykchalresp` をインストールしてください(12章参照) |
| YubiKey開錠が `wrong password or no matching key` | スロット番号が違う可能性があります。`--yubikey-slot` で明示するか、`ykman otp chalresp --generate 2` で設定したスロットと一致しているか確認 |
| YubiKeyのタッチ待ちでタイムアウトする | 30秒以内に光っているボタンにタッチしてください。USB接続や複数キー挿し込み時の認識も確認 |

---

## 付録: 最小チートシート

```sh
export BAKKU_PASSWORD='<パスワード>'

bakku init --repo <URL>                       # リポジトリ作成(最初に1回)
bakku dest add <名前> <URL>                    # 名前を付ける(最初に1回)
bakku backup <パス...> --repo <名前>           # バックアップ
bakku snapshots --repo <名前>                  # 一覧
bakku restore <ID> --repo <名前> --target <復元先>   # 復元
bakku forget --keep-daily 7 --prune --repo <名前>    # 整理
bakku check --read-data --repo <名前>          # 検証
```
