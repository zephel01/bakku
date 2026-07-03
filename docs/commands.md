# bakku コマンドリファレンス

全コマンドとオプションの意味の一覧です。実装(`bakku <command> --help`)と同期しています。
初めての方は [クイックガイド](quickguide.md) から読むのがおすすめです。

## 目次

- [グローバルオプション(全コマンド共通)](#グローバルオプション全コマンド共通)
- [環境変数一覧](#環境変数一覧)
- リポジトリ操作: [init](#bakku-init) / [backup](#bakku-backup) / [restore](#bakku-restore)
- 参照: [snapshots](#bakku-snapshots) / [ls](#bakku-ls) / [diff](#bakku-diff)
- メンテナンス: [forget](#bakku-forget) / [prune](#bakku-prune) / [check](#bakku-check) / [verify-restore](#bakku-verify-restore)
- 設定・鍵: [dest](#bakku-dest) / [key](#bakku-key) / [password](#bakku-password)
- 自動化: [schedule](#bakku-schedule)
- その他: [version](#bakku-version)

---

## グローバルオプション(全コマンド共通)

| オプション | 意味 |
|---|---|
| `--repo <dest名\|URL>` | 操作対象のリポジトリ。`dest add` で登録した名前、またはURL(`s3://` `sftp://` `gdrive://` `dropbox://` `smb://` `file://` / 絶対パス)を直接指定 |
| `--config <path>` | 設定ファイルのパス。省略時は `$BAKKU_CONFIG`、それも無ければ `~/.config/bakku/config.toml` |
| `--password-file <path>` | リポジトリパスワードをこのファイルの先頭行から読む |
| `--password-command <cmd>` | シェルコマンドを実行し、標準出力の先頭行をパスワードとして使う(例: `"op read op://Private/bakku/password"`) |
| `--yubikey` | パスワードの代わりに、登録済みYubiKeyのチャレンジレスポンスで開錠する |
| `--yubikey-slot <n>` | `key add --yubikey` 時に使うYubiKeyのOTPスロット番号(デフォルト2)。開錠時はkeyファイルに保存されたスロットが自動で使われるため指定不要 |
| `--json` | 対応コマンド(backup/snapshots/forget/prune/check/diff/verify-restore/key)で機械可読なJSONを出力。スクリプト連携用 |
| `-h, --help` | ヘルプ表示 |
| `-v, --version` | バージョン表示(ルートコマンドのみ) |

**パスワードの解決順**(上から順に試行、見つかった時点で確定):

1. 環境変数 `BAKKU_PASSWORD`
2. `--password-file`
3. `--password-command`
4. config の `password_command`(dest固有 → グローバルの順)
5. OSキーチェーン(`password store` で保存済みの場合)
6. 対話プロンプト

パスワード解決に全て失敗しても、リポジトリにYubiKeyスロットがありYubiKeyツールがPATHにあれば、自動でYubiKey開錠を試みます。

## 環境変数一覧

| 変数 | 意味 |
|---|---|
| `BAKKU_PASSWORD` | リポジトリパスワード(解決順の最優先) |
| `BAKKU_NEW_PASSWORD` | `key add` で追加する新スロットのパスワード |
| `BAKKU_CONFIG` | 設定ファイルパスの上書き |
| `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` | S3系の認証(AWS SDK標準チェーンの一部) |
| `BAKKU_SFTP_PASSWORD` | SFTPのパスワード認証(ssh-agent・鍵認証が使えない場合の最終手段) |
| `BAKKU_SSH_INSECURE=1` | SFTPのknown_hostsホスト鍵検証をスキップ(非推奨) |
| `BAKKU_GDRIVE_CREDENTIALS` | Google DriveのOAuthクライアントシークレットJSONのパス |
| `BAKKU_DROPBOX_TOKEN` | DropboxのAPIアクセストークン |
| `BAKKU_SMB_PASSWORD` | SMB共有のパスワード(NTLMv2) |

---

## bakku init

```
bakku init --repo <dest|URL>
```

新しいリポジトリを作成します。パスワードを解決(上記の順)し、マスターキーを生成して最初のパスワードスロットを書き込みます。既にリポジトリが存在する場所には作成できません。

固有オプション: なし(グローバルのみ)。

## bakku backup

```
bakku backup <paths...> --repo <dest|URL> [flags]
```

指定パスを増分バックアップし、スナップショットを1つ作成します。リポジトリに既存のチャンクはアップロードされません(重複排除)。

| オプション | 意味 |
|---|---|
| `--tag <t>` | スナップショットに付けるタグ。複数指定可(`--tag daily --tag docs`)。`forget --keep-tag` での保護や一覧での識別に使う |
| `--exclude <glob>` | 除外するパスのglobパターン。複数指定可(例: `--exclude '**/node_modules/**'`) |
| `--parallel <n>` | ワーカーgoroutine数。0(デフォルト)は自動 |
| `--no-notify` | この実行に限り、設定済みWebhook通知を送らない |
| `--use-vss` | (Windows) ボリュームシャドウコピーの使用を試みる。**現在は未実装のスタブ**: 警告を出してVSSなしで続行する |

出力: 処理ファイル数、新規/再利用チャンク数、新規バイト数、スナップショットID。

## bakku restore

```
bakku restore <snapshot-id> --repo <dest|URL> --target <dir> [flags]
```

スナップショットを指定ディレクトリへ復元します。スナップショットIDは先頭8桁程度の短縮形で指定可能です。

| オプション | 意味 |
|---|---|
| `--target <dir>` | **必須**。復元先ディレクトリ。既存ファイルへの直接上書きを避けるため、空/新規のディレクトリ推奨 |
| `--include <glob>` | 指定パターンに一致するファイルのみ復元。複数指定可(例: `--include '**/report.xlsx'`) |
| `--chown` | ファイル所有者(uid/gid)も復元する。root/Administrator実行時のみ有効、それ以外では黙って無視される |
| `--restore-quarantine` | (macOS) `com.apple.quarantine` 拡張属性も復元する。デフォルトでは除外(復元したアプリ/文書がGatekeeperに再ブロックされるのを防ぐため) |

## bakku snapshots

```
bakku snapshots --repo <dest|URL>
```

スナップショット一覧(短縮ID・日時・ホスト名・タグ・対象パス)を表示します。`--json` で全項目のJSON配列。

固有オプション: なし。

## bakku ls

```
bakku ls <snapshot-id> --repo <dest|URL>
```

スナップショット内のファイル・ディレクトリ・シンボリックリンクの一覧を表示します。

固有オプション: なし。

## bakku diff

```
bakku diff <snapshot-id1> <snapshot-id2> --repo <dest|URL>
```

2つのスナップショットを比較し、追加(`+`)・削除(`-`)・変更(`M`)されたパスと統計を表示します。古い方を第1引数にすると差分が直感的になります。

固有オプション: なし。

## bakku forget

```
bakku forget --repo <dest|URL> [--keep-* ...] [flags]
```

GFS(Grandfather-Father-Son)リテンションポリシーを適用し、ポリシー外のスナップショット記録を削除します。スナップショットはホスト+対象パス集合ごとにグループ化して選別されます。

| オプション | 意味 |
|---|---|
| `--keep-last <N>` | 最新のNスナップショットを保持 |
| `--keep-daily <N>` | 直近N日について、各日の最新1つを保持 |
| `--keep-weekly <N>` | 直近N週(ISO週)について、各週の最新1つを保持 |
| `--keep-monthly <N>` | 直近Nヶ月について、各月の最新1つを保持 |
| `--keep-yearly <N>` | 直近N年について、各年の最新1つを保持 |
| `--keep-tag <t>` | このタグを持つスナップショットを常に保持。複数指定可 |
| `--dry-run` | 削除対象を表示するだけで、実際には削除しない。**まずこれで確認するのを推奨** |
| `--prune` | 削除後に続けて `prune` を実行し、ディスク領域を回収する |

注意: `forget` だけではスナップショット記録が消えるだけで、データ領域は回収されません。`--prune` を付けるか、別途 `bakku prune` を実行してください。複数の `--keep-*` は併用でき、いずれかの条件で保持されたものは残ります。

## bakku prune

```
bakku prune --repo <dest|URL> [flags]
```

どのスナップショットからも参照されないデータを削除して領域を回収します。完全に未使用のpackは削除、一部だけ使用中のpackは生存データのみ新packへ再パックしてから削除します(crash-safe: 新pack書き込み→index差し替え→旧pack削除の順)。

| オプション | 意味 |
|---|---|
| `--dry-run` | 回収見込み(削除pack数・回収バイト数)を表示するだけで、リポジトリを変更しない |
| `--no-notify` | この実行に限りWebhook通知を送らない |

注意: prune実行中に同じリポジトリへ並行して `backup` しないでください(排他ロック機構は未実装)。

## bakku check

```
bakku check --repo <dest|URL> [flags]
```

リポジトリの整合性を検査します。index内全blobの所在、全スナップショットのtree走査、packファイルの存在を確認し、エラーは全件収集して最後に一覧表示します(エラーありなら終了コード1)。

| オプション | 意味 |
|---|---|
| `--read-data` | 全packを実際に読み、blobを復号してBLAKE3ハッシュを再計算・照合する。ビット腐敗(bitrot)も検出できる最も確実な検査。データ量に比例して時間がかかる |

## bakku verify-restore

```
bakku verify-restore <snapshot-id> --repo <dest|URL> [flags]
```

スナップショットからランダムに選んだファイルを一時ディレクトリへ実際に復元し、内容ハッシュを検証してから削除します。「復元できないバックアップ」を早期発見するためのコマンドです。

| オプション | 意味 |
|---|---|
| `--sample <pct>` | サンプリングするファイルの割合(%)。デフォルト10(最低10ファイルは選択される) |

## bakku dest

バックアップ先の名前(dest名→URL)を設定ファイルで管理します。

```
bakku dest add <name> <url>     # 追加または更新
bakku dest list                 # 一覧
bakku dest remove <name>        # 削除
```

登録後は `--repo <name>` で参照できます。固有オプション: なし。

## bakku key

鍵スロットを管理します。全スロットは同一のマスターキーをそれぞれの方式でラップしており、どれか1つで開錠できます(1つ失っても他で開ける「保険」)。

### bakku key add

```
bakku key add --repo <dest|URL> [flags]
```

既存の資格情報でリポジトリを開いた上で、新しいスロットを追加します。

| オプション | 意味 |
|---|---|
| (指定なし) | パスワードスロットを追加。新パスワードは `BAKKU_NEW_PASSWORD` → `--new-password-file` → 対話(2回入力)の順で取得 |
| `--new-password-file <path>` | 新スロットのパスワードをファイルから読む |
| `--yubikey` | パスワードの代わりにYubiKeyチャレンジレスポンススロットを追加。ykchalresp/ykman(PATH上にある方)で2回チャレンジし、応答が安定していることを確認してから保存 |
| `--yubikey-slot <n>` | 登録に使うYubiKeyのOTPスロット(デフォルト2)。事前に `ykman otp chalresp --generate 2` でHMAC-SHA1設定が必要 |

### bakku key list

```
bakku key list --repo <dest|URL>
```

スロット一覧(短縮ID・タイプ(password/yubikey-chalresp)・作成日時・現在開錠に使用中 `*`)を表示します。

### bakku key remove

```
bakku key remove <keyID> --repo <dest|URL> [flags]
```

スロットを削除します。**最後の1スロットは削除できません**。

| オプション | 意味 |
|---|---|
| `--force` | 現在開錠に使用中のスロット、またはパスワードスロットが1つも残らなくなる削除を強行する(YubiKey紛失時に復元不能になるため通常は非推奨) |

## bakku password

リポジトリパスワードをOSのシークレットストア(macOS Keychain / Windows資格情報マネージャー / Linux Secret Service)に保存・削除します。保存後は `--repo` 指定だけでパスワード入力なしに開錠できます。

```
bakku password store --repo <dest|URL>    # 保存(パスワードは通常の解決順で取得)
bakku password forget --repo <dest|URL>   # 削除
```

ヘッドレス環境等でシークレットストアが無い場合、storeは失敗しますが、通常コマンドの解決チェーンは黙って次の手段(対話等)へフォールバックします。固有オプション: なし。

## bakku schedule

OSネイティブのスケジューラ(Linux=systemd --user timer / macOS=launchd / Windows=タスクスケジューラ)に定期実行ジョブを登録します。

### bakku schedule install

```
bakku schedule install --name <job> --cron "<cron式>" -- <bakkuサブコマンド> [引数...]
```

| オプション | 意味 |
|---|---|
| `--name <job>` | **必須**。ジョブの一意な名前(英数字・`-`・`_`) |
| `--cron "<式>"` | **必須**。標準5フィールドcron式(`分 時 日 月 曜日`)。例: `"0 3 * * *"`=毎日03:00 |
| `-- <args...>` | `--` 以降がそのまま実行される bakku サブコマンドと引数。例: `-- backup ~/Documents --repo usb --password-file ~/.config/bakku/password` |

注意: スケジュール実行には環境変数が渡らないため、パスワードは `--password-file` 等で明示的に渡してください。Windowsのschtasksバックエンドは分・時が単一値のcron式のみ対応(複雑な式は明示的にエラー)。登録コマンドの実行に失敗した場合はユニット/plistファイルの生成までは行い、手動登録手順を表示します。

### bakku schedule uninstall

```
bakku schedule uninstall --name <job>
```

| オプション | 意味 |
|---|---|
| `--name <job>` | **必須**。削除するジョブ名 |

### bakku schedule status

```
bakku schedule status [--name <job>]
```

| オプション | 意味 |
|---|---|
| `--name <job>` | 指定したジョブのみ表示。省略時はbakku管理下の全ジョブを一覧 |

## bakku version

```
bakku version        # または bakku --version
```

バージョン・コミットハッシュ・ビルド日時を表示します。`go install` でビルドした場合は `dev` と表示されます(リリースバイナリはldflagsで焼き込み済み)。
