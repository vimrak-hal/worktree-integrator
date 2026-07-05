# 追加ファイルのコピー（copy）

`git worktree add` が新しい worktree に持ってくるのは追跡対象のファイルだけです。
`.gitignore` で除外している `.env` やローカルのデータディレクトリは、作成直後の
worktree には存在しません。`[defaults.copy]`（全リポジトリ共通）と
`[repos.<name>.copy]`（リポジトリ固有）で、元リポジトリの作業ツリーから新しい
worktree へコピーするものを指定できます。

```toml
# ~/.config/worktree-integrator/config.toml

[defaults.copy]                  # すべてのリポジトリに適用
paths = [".env"]

[repos.api]
copy = ["backend/.env", "frontend/.env"]   # 配列は paths の速記形

[repos.web.copy]                 # .gitignore を参照した自動コピー
gitignored = true
exclude    = ["*.log"]
```

## 指定できるキー

| キー | 説明 |
| --- | --- |
| `paths` | コピーする相対パスの配列。ファイルとディレクトリ（再帰コピー）の両方を指定できます。常にコピーされ、`exclude` の対象になりません。値全体を配列にすると `paths` だけの速記形になります |
| `gitignored` | `true` で、`.gitignore` により無視されているエントリ（`git ls-files -o -i` 相当）を自動コピーします。無視ディレクトリは丸ごと再帰コピーされます |
| `exclude` | 自動コピーから除外するパターン。gitignore と同じ書式です（`/` を含まない名前はどの階層でも一致、`/` を含むパターンはリポジトリルート起点、末尾 `/` はディレクトリのみに一致） |
| `exclude_defaults` | 下記の組み込みの既定除外を適用するか（既定 `true`） |

## 組み込みの既定除外

`gitignored = true` のとき、依存やビルド成果物を worktree ごとに複製しないよう、
次のパターンが既定で除外されます:

`node_modules`・`.venv`・`venv`・`target`・`.direnv`・`.cache`・`.DS_Store`

これらもコピーしたい場合は `exclude_defaults = false` を指定します。`[defaults.copy]`
と `[repos.<name>.copy]` の両方に書かれている場合は、リポジトリ側の指定が優先されます。

## defaults とリポジトリ設定のマージ

`[defaults.copy]` と `[repos.<name>.copy]` は次のようにマージされます:

- `paths`: 両方を連結します（defaults が先、重複は除去）。
- `gitignored`: どちらかが `true` なら有効です。
- `exclude`: 両方の和集合に、組み込みの既定除外（オプトアウトしていなければ）を
  加えたものです。

## 動作

- コピーは worktree の**新規作成直後**（`after_worktree` フックより前）に実行されます。
  既存としてスキップされたリポジトリではコピーされません。
- 元リポジトリに存在しないパスは黙ってスキップされます（全リポジトリに同じファイルが
  ある必要はありません）。
- コピーはベストエフォートです。失敗しても worktree の作成は失敗扱いにならず、進捗に
  警告として報告されます。コピーした項目は進捗に `コピー: <パス>` と表示されます。
- **安全性**: 絶対パス・`..` を含むパス・`.git` 配下のパスは拒否されます。コピーは
  シンボリックリンクを辿らないため、worktree の外を読み書きすることはありません
  （リンク自体はリンクとして再作成されます）。
- コピー対象は gitignore された未追跡ファイル向けです。追跡済みのファイルを指定すると、
  worktree が持ってきた内容を作業ツリー側の内容で上書きしてしまいます。
