# 設定ファイル

設定は TOML 形式で `~/.config/worktree-integrator/config.toml` に記述します
（正確には `$XDG_CONFIG_HOME/worktree-integrator/config.toml`）。ファイルが無ければ
組み込みの既定値で動作します。すべてのキーは省略可能です。

!!! tip "推奨: Claude Code プラグインで対話的に作成"
    未知キーは設定全体が読み込まれないパースエラーになる・`[repos.<name>]` は
    `repos_dir` 直下のディレクトリ名と厳密一致が必要、など手書きには詰まりどころが
    あります。Claude Code を使っているなら、環境を確認しながら設定を組み立てて
    `config check` での検証まで案内する[公式プラグイン](claude-code-plugin.md)での
    作成が推奨です。

## 全体像

```toml
# ~/.config/worktree-integrator/config.toml

repos_dir     = "~/repositories"    # 対象リポジトリのベースディレクトリ
worktrees_dir = "~/worktrees"       # worktree 作成先のベースディレクトリ
remote        = "origin"            # fetch 対象の remote
concurrency   = 0                   # worktree 並列作成の上限（0 = 自動）

[defaults]                          # 全リポジトリ共通の既定値
base = "auto"                       # ベースブランチ

[defaults.copy]                     # 全リポジトリ共通のコピー設定 → Copy Files
paths = [".env"]

[[hooks.before]]                    # ライフサイクルフック → Hooks
name    = "example"
command = "echo $WT_WORKTREE_NAME"

[repos.api]                         # リポジトリ単位の設定（キーは repos_dir 直下の名前）
base = "develop"

[repos.api.copy]                    # リポジトリ固有のコピー設定 → Copy Files
paths = ["backend/.env"]

[repos.api.servers.backend]         # dev サーバー定義 → Server Management
start = "bin/rails server"
```

## トップレベルのキー

| キー | 既定値 | 説明 |
| --- | --- | --- |
| `repos_dir` | `~/repositories` | 対象リポジトリのベースディレクトリ（`~` は展開されます） |
| `worktrees_dir` | `~/worktrees` | worktree 作成先のベースディレクトリ |
| `remote` | `origin` | fetch 対象の remote |
| `concurrency` | `0`（自動） | worktree 並列作成の上限。自動時はリポジトリ数と CPU コア数から決まります |

## `[defaults]` と `[repos.<name>]`

`[defaults]` は全リポジトリ共通の既定値、`[repos.<name>]` はリポジトリ単位の設定です。

**`base`** は worktree の作成元になるベースブランチで、次の順に解決されます:

```
--base フラグ ＞ [repos.<name>].base ＞ [defaults].base ＞ "auto"
```

`auto`（既定値）はリモートのデフォルトブランチ（`refs/remotes/<remote>/HEAD`）→
`main` → `master` の順に、ローカルの ref だけを見て解決します（ネットワークアクセスは
発生しません）。

**`copy`**（追加ファイルのコピー）は [Copy Files](copy-files.md)、
**`servers`**（dev サーバー定義）は [Server Management](server-management.md) を
参照してください。

## 優先順位

すべての設定で共通です:

```
コマンドラインフラグ ＞ 環境変数 ＞ 設定ファイル ＞ 組み込みの既定値
```

フラグと環境変数の対応表は [Usage](usage.md) にあります。

## 検証

未知のキーはエラーとして報告され、設定全体が読み込まれません。`wt config check` で、
設定ファイルの構文と必須フィールドをまとめて検証できます。
