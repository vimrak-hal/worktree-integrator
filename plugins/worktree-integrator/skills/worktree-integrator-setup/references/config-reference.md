# config.toml リファレンス（スキーマ v2）

設定ファイルの場所は固定: `$XDG_CONFIG_HOME/worktree-integrator/config.toml`
（`XDG_CONFIG_HOME` 未設定時は `~/.config/worktree-integrator/config.toml`）。
ファイルが無ければ組み込み既定値で動作する。すべてのキーは省略可能。

解決の優先順位（全項目共通）:

```
コマンドラインフラグ / MCP パラメータ ＞ 環境変数 ＞ 設定ファイル ＞ 組み込み既定値
```

未知キーは拒否され、**設定全体が読み込まれない**（既定値へのフォールバックではなく
エラーで停止）。書き換えたら `worktree-integrator config check` で必ず検証する。

## 全体像

```toml
# ~/.config/worktree-integrator/config.toml

repos_dir     = "~/repositories"    # 対象リポジトリのベースディレクトリ
worktrees_dir = "~/worktrees"       # worktree 作成先のベースディレクトリ
remote        = "origin"            # fetch 対象の remote
concurrency   = 0                   # worktree 並列作成の上限（0 = 自動）

[defaults]                          # 全リポジトリ共通の既定値
base = "auto"                       # ベースブランチ

[defaults.copy]                     # 全リポジトリ共通のコピー設定
paths = [".env"]

[[hooks.before]]                    # ライフサイクルフック
name    = "example"
command = "echo $WT_WORKTREE_NAME"

[repos.api]                         # リポジトリ単位の設定（キーは repos_dir 直下の名前）
base = "develop"

[repos.api.copy]                    # リポジトリ固有のコピー設定
paths = ["backend/.env"]

[repos.api.servers.backend]         # dev サーバー定義
start = "bin/rails server"
```

トップレベルに置けるのは `repos_dir` / `worktrees_dir` / `remote` / `concurrency` と
`[defaults]` / `[[hooks.*]]` / `[repos.<name>]` のみ。

## トップレベルのキー

| キー | 既定値 | 説明 |
| --- | --- | --- |
| `repos_dir` | `~/repositories` | 対象リポジトリのベースディレクトリ（`~` は展開される） |
| `worktrees_dir` | `~/worktrees` | worktree 作成先。`<worktrees_dir>/<worktree名>/<リポジトリ名>/` に作られる |
| `remote` | `origin` | fetch 対象の remote |
| `concurrency` | `0`（自動） | worktree 並列作成の上限。自動時はリポジトリ数と CPU コア数から決まる |

## `base` — ベースブランチの解決

`[defaults].base`（全リポジトリ共通）と `[repos.<name>].base`（リポジトリ別）で指定。
解決順:

```
--base フラグ / MCP の base ＞ [repos.<name>].base ＞ [defaults].base ＞ "auto"
```

`auto`（既定）はリモートのデフォルトブランチ（`refs/remotes/<remote>/HEAD`）→
`main` → `master` の順に、**ローカルの ref だけを見て**解決する（ネットワークアクセス
なし）。fetch は `git fetch --no-tags <remote> <base>` で対象ブランチ 1 本のみ。
fetch に失敗しても既存の追跡 ref があれば続行するため、オフラインでも動く。

## `[repos.<name>.servers.<server>]` — dev サーバー定義

`<name>` は **`repos_dir` 直下のディレクトリ名**（不一致だと `server_*` の対象に
ならない）。`<server>` は任意の名前で、1 リポジトリに複数定義できる。

| キー | 必須/既定 | 説明 |
| --- | --- | --- |
| `start` | **必須** | 起動コマンド。独立セッションとして detach 起動され、CLI/MCP 終了後も動き続ける |
| `dir` | worktree ルート | サーバーを動かす worktree 内のサブディレクトリ（相対パス） |
| `setup` | – | その worktree を**初めて**有効化したときだけ実行 |
| `on_activate` | – | 有効化のたびに `start` の直前に実行 |
| `on_switch` | – | **初期化済み**の worktree に切り替えたときだけ実行 |
| `stop_grace_secs` | `5` | 停止時に SIGTERM → SIGKILL へ昇格するまでの秒数 |

- 実行順: 初回有効化は `setup` → `on_activate` → `start`、切替（初期化済み）は
  `on_switch` → `on_activate` → `start`。`start` は最後に detach 起動。
- `start` / `setup` / `on_activate` / `on_switch` は単一の文字列（`&&` やパイプ可）か
  文字列の配列（記述順に逐次実行 = 内部的に `&&` 連結）。
- 作業ディレクトリは `<worktree>/<dir>`。
- 停止はプロセスグループ全体へシグナルを送るため、`npm run dev` のような子プロセスを
  伴うサーバーも丸ごと止まる。
- あるサーバーのコマンドが失敗してもそのサーバーが「失敗」と報告されるだけで、他の
  サーバー・他のリポジトリの処理は続行する。

環境変数: hooks と同じ `WT_*`（下記）に加えて `WT_SERVER_NAME`（サーバー名）。

## `[[hooks.<timing>]]` — ライフサイクルフック

同じタイミングのフックは**並列**実行。

| タイミング | 実行されるとき | 作業ディレクトリの既定 | 失敗時 |
| --- | --- | --- | --- |
| `before` | リポジトリ探索の前に全体で 1 回 | 呼び出し時のディレクトリ | 全体を中断 |
| `after_worktree` | 新規作成された worktree ごとに 1 回 | その worktree | 続行して exit 非ゼロ |
| `after` | 全処理の後に全体で 1 回（`enter` でも実行） | 呼び出し時のディレクトリ | 続行して exit 非ゼロ |

| キー | 必須/既定 | 説明 |
| --- | --- | --- |
| `name` | **必須** | 進捗・サマリ表示に使う名前 |
| `command` | **必須** | `sh -c` で解釈。単一文字列か文字列配列（逐次実行、途中失敗で打ち切り） |
| `allow_failure` | `false` | `true` で失敗しても警告どまり |
| `workdir` | タイミングごとの既定 | 作業ディレクトリの明示指定 |
| `timeout_secs` | `0`（無制限） | 最大実行時間（秒）。超過で強制終了 |

> `background` キーは廃止済み（指定するとパースエラー）。常駐プロセスは
> `[repos.<name>.servers.<server>]` で管理する。

### フック・サーバーコマンドに渡る環境変数

| 変数 | 内容 |
| --- | --- |
| `WT_WORKTREE_NAME` | worktree 名（= ブランチ名） |
| `WT_REPOS_DIR` | 対象リポジトリのベースディレクトリ |
| `WT_WORKTREES_DIR` | worktree 作成先のベースディレクトリ |
| `WT_ROOT` | 今回の worktree ルート（`<worktrees_dir>/<worktree名>`） |
| `WT_REPO_NAME` | リポジトリ名（`after_worktree` とサーバーコマンドのみ） |
| `WT_REPO_PATH` | 元リポジトリのパス（同上） |
| `WT_WORKTREE_PATH` | そのリポジトリの worktree パス（同上） |
| `WT_SERVER_NAME` | サーバー名（サーバーのライフサイクルコマンドのみ） |

## `[defaults.copy]` / `[repos.<name>.copy]` — 追加ファイルのコピー

`git worktree add` は追跡対象しか実体化しないため、gitignore された `.env` 等を
worktree に引き継ぐための仕組み。

| キー | 既定 | 説明 |
| --- | --- | --- |
| `paths` | `[]` | 常にコピーする明示的な相対パス（除外の対象にならない） |
| `gitignored` | `false` | `true` で gitignore された未追跡エントリを丸ごとコピー |
| `exclude` | `[]` | gitignored モードでの追加除外パターン（gitignore 互換） |
| `exclude_defaults` | `true` | 組み込み既定除外を適用するか |

- 速記形: パスのリストだけなら `copy = [".env", "config/local.yml"]` と書ける。
- 組み込み既定除外: `node_modules` / `.venv` / `venv` / `target` / `.direnv` /
  `.cache` / `.DS_Store`（`exclude_defaults = false` でオプトアウト）。
- マージ規則: `paths` は defaults → repo の順で重複排除して連結。`gitignored` は
  両者の OR。`exclude` は和集合。`exclude_defaults` は repo 側の明示指定が defaults
  より優先（どちらも未指定なら適用 = 安全側）。

## フラグ / 環境変数の対応

| フラグ | 環境変数 | 既定値 | 対応コマンド |
| --- | --- | --- | --- |
| `--repos-dir` | `WT_REPOS_DIR` | `~/repositories` | `create` / `server *` |
| `--worktrees-dir` | `WT_WORKTREES_DIR` | `~/worktrees` | `create` / `server *` |
| `--remote` | `WT_REMOTE` | `origin` | `create` |
| `--concurrency`, `-j` | `WT_CONCURRENCY` | 自動 | `create` |
| `--base` | – | `auto` | `create` |

その他のコマンド（`list` / `enter` / `remove` / `doctor` / `repos` / `alias`）は
設定ファイルと環境変数からディレクトリを解決する（フラグは受け付けない）。

## CLI コマンド一覧（参考）

```sh
worktree-integrator <name>                # ≡ create <name>（対話選択）
worktree-integrator create <name> [--repo <repo>]... [--all] [--base <ref>] [-j N] [--json]
worktree-integrator list   [--json]
worktree-integrator enter  <name>         # after フックだけ実行（移動用）
worktree-integrator remove <name> [--force] [--keep-branch] [--json]
worktree-integrator repos [--json]
worktree-integrator server switch <name> [--repo <repo>]... [--require-worktree] [--restart] [--json]
worktree-integrator server status [--repo <repo>]... [--json]
worktree-integrator server stop   [<name>] [--repo <repo>]... [--json]
worktree-integrator server logs   [<name>] [--repo <repo>]... [-n N] [-f] [--prev] [--json]
worktree-integrator alias set <name> <label> | alias list [--json] | alias remove <name>
worktree-integrator doctor [--fix] [--json]
worktree-integrator config check
worktree-integrator mcp
```

- `create` は冪等: 同じ名前で再実行すると既存はスキップし、未作成分だけ差分追加する。
- worktree 名 = ブランチ名。`git check-ref-format` 準拠（`/` 区切り可、各セグメントは
  ASCII 英数字・`.`・`_`・`-`、`..` や先頭 `-` / `.`、`.lock` 終わりは不可）。
- 非対話環境（パイプ・CI・MCP）では `--repo` か `--all`（MCP は `repos` 必須）。
- MCP の `worktree_remove` に `--force` 相当は無い（強制削除は CLI のみ）。
- 終了コード: `0` 成功 / `1` エラー / `130` キャンセル（Ctrl-C）。

## 状態とログの保存先（自動生成・手で作らない）

`$XDG_STATE_HOME/worktree-integrator/`（未設定時 `~/.local/state/worktree-integrator/`）:

- `servers.toml` — サーバーごとの稼働記録（PID・active worktree・初期化済み一覧）
- `aliases.toml` — worktree の別名
- `logs/<repo>__<server>__<worktree>.log` — サーバーの標準出力／標準エラー。起動の
  たびに既存ログは `.log.prev` へローテーション（1 世代保持）
