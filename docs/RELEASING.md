# リリース手順 / Releasing

## バージョニングポリシー (SemVer)

`vMAJOR.MINOR.PATCH` の [Semantic Versioning](https://semver.org/lang/ja/) に従います。

| 上げる桁 | 条件 |
|---|---|
| MAJOR | リポジトリフォーマットの後方非互換変更(古いbakkuで読めなくなる)、CLIの破壊的変更 |
| MINOR | 機能追加(新コマンド・新バックエンド・新フラグ)、後方互換のフォーマット拡張 |
| PATCH | バグ修正・ドキュメント・依存更新のみ |

補足ルール:

- **0.x の間**は SemVer 慣例どおり MINOR が破壊的変更を含み得ます。安定化後に v1.0.0 を宣言します。
- **リポジトリフォーマット互換性が最優先**。フォーマットに触れる変更は `config` 内の `version` フィールドを必ず確認し、旧バージョンで作成したリポジトリの読み取り互換を維持するか、MAJOR を上げて移行手順を用意すること。
- コミットは Conventional Commits(`feat:` / `fix:` / `docs:` / `refactor:` など)。`feat!:` や `BREAKING CHANGE:` フッターは MAJOR(0.x では MINOR)相当の印。

## リリース手順

1. **CHANGELOG更新**: `CHANGELOG.md` の `[Unreleased]` の内容を新バージョン見出し `[X.Y.Z] - YYYY-MM-DD` に移し、末尾の比較リンクを更新する。
2. **コミット**: `docs: prepare vX.Y.Z release` 等でコミットし、mainに取り込む(通常はfeature branch + PR経由)。
3. **タグ付け**(annotated tag):

   ```sh
   git tag -a vX.Y.Z -m "bakku vX.Y.Z"
   git push origin main --follow-tags
   ```

4. **自動リリース**: `v*` タグのpushでGitHub Actionsの `release` ジョブが起動し、`scripts/build-release.sh` が6ターゲット(darwin/linux/windows × amd64/arm64)のバイナリとchecksumsをビルドしてGitHub Releasesに添付する。
5. **確認**: Releasesページの成果物、`bakku version` の出力(version/commit/dateが `-ldflags` で焼き込まれる)、CHANGELOGリンクを確認する。

ローカルで同じ成果物を作る場合:

```sh
scripts/build-release.sh vX.Y.Z   # dist/ に6バイナリ + checksums
```

## タグ運用

- タグは必ず **annotated tag**(`git tag -a`)。lightweight tagは使わない。
- タグは一度pushしたら動かさない(force-push禁止)。修正はPATCHリリースで。
- プレリリースは `vX.Y.Z-rc.1` 形式。CIのreleaseジョブは `v*` にマッチするため同様にビルドされる(GitHub Releases上でPre-releaseに手動マーク)。
