# bakku 事例集 & トラブルシューティング

実際の利用シーンごとのコピペレシピ集です。コマンドの全オプションは [コマンドリファレンス](commands.md)、初期設定は [クイックガイド](quickguide.md) を参照してください。

> 例では dest 名 `usb`、スナップショットID `f153023a` を使っています。自分の環境に読み替えてください。
> zsh(macOS標準)は対話シェルで行内 `#` コメントを解釈しないため、本書のコードブロックにはコメントを入れていません。

## 目次

- [事例集](#事例集)
  1. [バックアップの中身を確認する](#1-バックアップの中身を確認する)
  2. [ファイルを1つだけ取り出す](#2-ファイルを1つだけ取り出す)
  3. [前回から何が変わったか見る](#3-前回から何が変わったか見る)
  4. [バックアップが正しく復元できるか定期確認する](#4-バックアップが正しく復元できるか定期確認する)
  5. [パスワード入力を省略する](#5-パスワード入力を省略する)
  6. [容量を節約する(除外設定と世代整理)](#6-容量を節約する除外設定と世代整理)
  7. [別のPCから同じリポジトリを参照する](#7-別のpcから同じリポジトリを参照する)
  8. [リポジトリごと別ストレージへ引っ越す](#8-リポジトリごと別ストレージへ引っ越す)
  9. [誤って消したファイルを過去のバックアップから探す](#9-誤って消したファイルを過去のバックアップから探す)
  10. [1つ前・2つ前の世代からファイルを取り出す](#10-1つ前2つ前の世代からファイルを取り出す)
- [トラブルシューティング](#トラブルシューティング)

---

## 事例集

### 1. バックアップの中身を確認する

一覧を見る(ファイル数が多い場合は less や grep で絞る):

```sh
bakku ls f153023a --repo usb | less
bakku ls f153023a --repo usb | grep 'CodeRouter/'
bakku ls f153023a --repo usb | grep '\.go$'
```

ファイルの中身まで見たい場合は、対象を絞って一時ディレクトリへ復元します:

```sh
bakku restore f153023a --repo usb --target /tmp/peek --include 'CodeRouter/**'
open /tmp/peek
rm -rf /tmp/peek
```

スナップショットIDは `bakku snapshots --repo usb` で確認できます(先頭8桁の短縮形で指定可)。

### 2. ファイルを1つだけ取り出す

`--include` は `**` 対応のglobです。ファイル名だけ指定すればどの階層にあってもマッチします:

```sh
bakku restore f153023a --repo usb --target /tmp/rescue --include '**/settings.json'
find /tmp/rescue -type f
```

同名ファイルが複数ヒットする場合はパスを含めて絞ります:

```sh
bakku restore f153023a --repo usb --target /tmp/rescue --include 'CodeRouter/config/settings.json'
```

### 3. 前回から何が変わったか見る

```sh
bakku snapshots --repo usb
bakku diff <古いID> <新しいID> --repo usb | less
```

出力は `+`(追加)/`-`(削除)/`M`(変更)。「バックアップしたつもりのファイルが入っているか」の確認にも使えます。

### 4. バックアップが正しく復元できるか定期確認する

「取れているつもりで復元できない」が最悪のパターンです。月1回程度、次の2つを回すことを推奨します:

```sh
bakku check --repo usb --read-data
bakku verify-restore <最新ID> --repo usb --sample 5
```

- `check --read-data`: 全データを読み戻してハッシュ照合(ビット腐敗も検出)。データ量に比例して時間がかかる
- `verify-restore`: ランダムサンプルを実際に復元して検証。ファイル数が多いリポジトリでは `--sample 5` 程度で十分

スケジュール化する場合:

```sh
bakku schedule install --name weekly-check --cron "0 4 * * 0" -- check --repo usb --read-data --password-file ~/.config/bakku/password
```

### 5. パスワード入力を省略する

**macOSキーチェーン(推奨・Mac/Windows)**:

```sh
bakku password store --repo usb
```

以後は入力プロンプトなしで開錠されます。解除は `bakku password forget --repo usb`。

**1Password連携**:

```sh
bakku backup ~/works --repo usb --password-command "op read op://Private/bakku/password"
```

毎回フラグを付けたくない場合は `~/.config/bakku/config.toml` に書きます:

```toml
password_command = "op read op://Private/bakku/password"
```

**環境変数/ファイル**: `export BAKKU_PASSWORD=...`(そのシェル内のみ)、または `--password-file`。

### 6. 容量を節約する(除外設定と世代整理)

開発ディレクトリでは生成物を除外すると新規データ量が大きく減ります:

```sh
bakku backup ~/works/project --repo usb \
  --exclude '**/node_modules/**' \
  --exclude '**/.git/**' \
  --exclude '**/target/**' \
  --exclude '**/dist/**' \
  --exclude '**/*.log'
```

`**` はディレクトリ階層をまたいでマッチします(v0.2.2以降)。`/` を含まないパターン(`*.log`)はファイル名にもマッチします。

古い世代の整理(まず `--dry-run` で確認してから):

```sh
bakku forget --repo usb --keep-daily 7 --keep-weekly 4 --keep-monthly 6 --dry-run
bakku forget --repo usb --keep-daily 7 --keep-weekly 4 --keep-monthly 6 --prune
```

`forget` だけでは容量は戻りません。`--prune` を忘れずに。

### 7. 別のPCから同じリポジトリを参照する

リポジトリはどのマシンからも同じURLとパスワードで開けます。別PCにbakkuをインストールして:

```sh
bakku dest add usb sftp://user@backup-host/backups/repo
bakku snapshots --repo usb
bakku restore <ID> --repo usb --target ~/restored
```

スナップショットにはホスト名が記録されるので、複数マシンで同じリポジトリにバックアップしても `snapshots` の HOST 列で見分けられます。

### 8. リポジトリごと別ストレージへ引っ越す

リポジトリはただのファイル群なので、丸ごとコピーすれば移行できます:

```sh
rsync -a /Volumes/OldDisk/bakku-repo/ /Volumes/NewDisk/bakku-repo/
bakku check --repo /Volumes/NewDisk/bakku-repo --read-data
bakku dest add usb /Volumes/NewDisk/bakku-repo
```

コピー後は必ず `check --read-data` で検証してから旧側を消してください。クラウドへの引っ越しも同様に(`rclone` 等でコピー→check)。

### 9. 誤って消したファイルを過去のバックアップから探す

どのスナップショットに入っているか総当たりで探します:

```sh
for id in $(bakku snapshots --repo usb --json | grep -o '"id": *"[a-f0-9]*"' | cut -d'"' -f4 | cut -c1-8); do
  echo "== $id =="
  bakku ls $id --repo usb | grep '消したファイル名'
done
```

見つかったら該当IDから部分復元します(事例2参照)。

### 10. 1つ前・2つ前の世代からファイルを取り出す

`bakku snapshots` は新しい順に並ぶため、行番号で世代を指定できます(1行目=ヘッダ、2行目=最新、3行目=1つ前、4行目=2つ前):

```sh
bakku snapshots --repo usb
```

シェル変数に取ると便利です:

```sh
LATEST=$(bakku snapshots --repo usb | sed -n 2p | awk '{print $1}')
PREV=$(bakku snapshots --repo usb | sed -n 3p | awk '{print $1}')
PREV2=$(bakku snapshots --repo usb | sed -n 4p | awk '{print $1}')
```

各世代から同じファイルを取り出して比較する例:

```sh
bakku restore $PREV --repo usb --target /tmp/gen1 --include '**/settings.json'
bakku restore $PREV2 --repo usb --target /tmp/gen2 --include '**/settings.json'
diff -r /tmp/gen1 /tmp/gen2
```

「どの世代でファイルが変わったか」を先に知りたい場合は復元せずに diff で確認できます:

```sh
bakku diff $PREV $LATEST --repo usb | grep settings.json
```

注意点が2つあります。同じリポジトリに複数のバックアップ対象(例: `CodeRouter` と `works/project` 全体)を混在させている場合、一覧には全て混ざって並ぶため、`bakku snapshots --repo usb | grep 'works/project'` のようにPATHS列で絞ってから行を数えてください。また、`forget` で世代整理をすると古い世代は消えるため、「◯世代前まで戻れる」ことを保証したい場合はリテンション設定(事例6)の `--keep-last` / `--keep-daily` をその世代数以上に設定します。

---

## トラブルシューティング

| 症状 | 原因と対処 |
|---|---|
| `wrong password or no matching key` | パスワード誤り。`BAKKU_PASSWORD` が古い値のまま残っていないか確認(`echo $BAKKU_PASSWORD`)。環境変数はファイル指定より優先される |
| 修正したはずのバグが再現する / 挙動が古い | **旧バイナリを実行している**。`bakku version` と `which bakku` で確認。`~/go/bin` と `/usr/local/bin` 等に複数のbakkuがあるとPATHの先勝ち。`go install github.com/zephel01/bakku/cmd/bakku@latest` で更新 |
| macOSで `operation not permitted` | フルディスクアクセス未許可。システム設定 → プライバシーとセキュリティ → フルディスクアクセスにターミナル(またはbakku)を追加 |
| `--exclude` が効いていない気がする | パターンを確認: 階層をまたぐには `**/名前/**`(v0.2.2以降)。実際に何が入ったかは `bakku ls <ID> \| grep 名前` で検証できる。なお backup サマリの `files:` は除外前のスキャン数を含む場合がある(既知の表示課題) |
| zshで `unknown file attribute` や `command not found: #` | zshの対話シェルは行内 `#` コメントを解釈しない。コメント付きのコマンド例を貼るときはコメントを削るか、`setopt interactive_comments` を実行しておく |
| macOSで `head: illegal byte count -- 1M` | BSD版headは `-c 1M` 非対応。`dd if=... bs=1m count=1` か `head -c 1048576` を使う(bakku自体とは無関係、テストデータ作成時の注意) |
| SFTPで `knownhosts: key is unknown` | 一度 `ssh user@host` で接続してホスト鍵を登録。検証を省略する場合のみ `BAKKU_SSH_INSECURE=1`(非推奨) |
| スケジュールが動かない | `bakku schedule status` で登録確認。スケジュール実行には環境変数が渡らないため、パスワードは `--password-file` で渡す。PCがその時刻にスリープしていないかも確認 |
| バックアップが遅い / 初回だけ重い | 初回はフルアップロードのため時間がかかる(2回目以降は差分のみ)。`--parallel <n>` で並列度調整。除外設定(事例6)で対象を減らすのも有効 |
| `forget` したのに容量が減らない | スナップショット記録が消えるだけでは領域は戻らない。`bakku prune` を実行する |
| prune中にbackupしてよい? | 不可。排他ロック機構は未実装のため、同一リポジトリへの並行実行は避ける |
| Google Driveで認証エラー | `~/.config/bakku/gdrive-token.json` を削除して再認証。日次アップロード上限(750GB)にも注意 |
| 復元先に既存ファイルがある | bakkuは復元先に直接書き込む。安全のため空のディレクトリへ復元してから移動する運用を推奨 |

解決しない場合は [GitHub Issues](https://github.com/zephel01/bakku/issues) へ。`--json` 出力やエラーメッセージ全文を添えてもらえると調査が早くなります。
