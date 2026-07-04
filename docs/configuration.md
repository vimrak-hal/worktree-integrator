# 設定ファイル

既定のディレクトリなどは `~/.config/worktree-integrator.toml` に TOML 形式で記述して
変更できます。ファイルが無い場合は組み込みの既定値が使われます。すべてのキーは任意で、
書かなかったキーは既定値にフォールバックします。

```toml
# ~/.config/worktree-integrator.toml
repos_dir     = "/srv/repos"        # 対象リポジトリのベースディレクトリ
worktrees_dir = "/srv/worktrees"    # worktree 作成先のベースディレクトリ
remote        = "upstream"          # fetch 対象の remote
```

> 並列実行数（`concurrency`）は設定ファイルでは指定できません。[Behavior](behavior.md) のとおり
> 自動で決定され、必要なときだけ `-j` で上書きします。

設定値の優先順位は次のとおりです（左ほど優先）：

```
コマンドラインフラグ ＞ 環境変数 ＞ 設定ファイル ＞ 組み込みの既定値
```

つまり設定ファイルに書いた値は組み込みの既定値を上書きし、必要なときだけフラグや
環境変数でさらに上書きできます。

このファイルには [Hooks](hooks.md)・[Copy Files](copy-files.md)・[Server Management](server-management.md)
の設定も記述します。各セクションを参照してください。
