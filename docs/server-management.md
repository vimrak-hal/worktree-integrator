# サーバー管理（`server` サブコマンド）

リポジトリごとに定義した dev サーバーを、**どの worktree で動かすか**という単位で
切り替える機能です。1 つのリポジトリに複数の名前付きサーバー（例: `backend` と
`frontend`）を定義でき、`server switch` はそのリポジトリの全サーバーをまとめて
切り替えます。切り替えると、元の worktree で動いていたサーバーは停止します。

## サーバーの定義

設定ファイルの `[repos.<リポジトリ名>.servers.<サーバー名>]` に定義します
（`<リポジトリ名>` は `repos_dir` 直下のディレクトリ名）:

```toml
# ~/.config/worktree-integrator/config.toml

[repos.rails-tutorial.servers.backend]
start       = "bin/rails server -p 3000"
dir         = "backend"
setup       = ["bundle install", "bin/rails db:migrate"]
on_activate = "lsof -ti:3000 | xargs -r kill"
on_switch   = "bin/rails db:migrate"

[repos.rails-tutorial.servers.frontend]
start = "npm run dev"
dir   = "frontend"
setup = ["npm ci"]
```

| キー | 既定値 | 説明 |
| --- | --- | --- |
| `start` | （必須） | サーバーの起動コマンド。独立したセッションとして detach 起動され、CLI 終了後も動き続けます |
| `dir` | worktree ルート | サーバーを動かす worktree 内のサブディレクトリ |
| `setup` | – | その worktree を**初めて**有効化したときだけ実行（依存インストール・DB 初期化など） |
| `on_activate` | – | 有効化のたびに、`start` の直前に実行 |
| `on_switch` | – | **初期化済み**の worktree に切り替えたときだけ実行 |
| `stop_grace_secs` | `5` | 停止時に SIGTERM から SIGKILL へエスカレーションするまでの秒数 |

ライフサイクルコマンドの実行順（`start` は最後に detach 起動）:

- **初回有効化**: `setup` → `on_activate` → `start`
- **切り替え（初期化済み）**: `on_switch` → `on_activate` → `start`

各コマンドはフックと同じく文字列または文字列の配列（逐次実行）で書けます。作業
ディレクトリは `<worktree>/<dir>` で、フックと同じ `WT_*` 環境変数
（[Hooks](hooks.md) を参照）に加えて `WT_SERVER_NAME` が渡されます。あるサーバーの
コマンドが失敗するとそのサーバーは「失敗」として報告され、他のサーバー・他の
リポジトリの処理は続行します。

停止はプロセスグループ全体へシグナルを送るため、`npm run dev` のような子プロセスを
伴うサーバーも丸ごと停止できます。

## `server switch` — worktree の切り替え

```sh
wt server switch <name> [--repo <repo>]... [--require-worktree] [--restart] [--json]
```

対象リポジトリの全サーバーを `<name>` の worktree へ切り替えます。既定の対象は
「`<name>` の worktree が存在する、設定済みのすべてのリポジトリ」で、`--repo` で
絞り込めます。

- worktree が無いリポジトリは通知してスキップします。`--require-worktree` を付けると
  エラーになります。
- 既に同じ worktree で稼働中のサーバーはそのまま維持します。`--restart` で再起動します。
- 別名 `activate` でも呼べます。

## `server status` — 状態の一覧

```sh
wt server status [--repo <repo>]... [--json]
```

リポジトリ・サーバーごとに、active な worktree・その[別名](#alias)・稼働状態
（稼働中 / 停止 / クラッシュ）・PID を一覧表示します。別名 `ls`。

## `server stop` — 停止

```sh
wt server stop [<name>] [--repo <repo>]... [--json]
```

稼働中のサーバーを停止します。`<name>` を省略するとすべて、指定すると active な
worktree がその名前のリポジトリだけが対象です。停止したリポジトリの active は
解除されます。

## `server logs` — ログの表示

```sh
wt server logs [<name>] [--repo <repo>]... [-n N] [-f] [--prev] [--json]
```

サーバーのログを表示します。`<name>` を省略すると現在稼働中のサーバーのログ、
指定するとその worktree のログです（停止後・クラッシュ後も閲覧できます）。

- `-n` — 末尾の行数（既定 50）
- `-f` — 追従します（`tail -f`）。`--json` とは併用できません
- `--prev` — 1 世代前のログ（サーバー起動時にローテーションされたもの）を表示します
- `--json` — 結果を JSON で出力します（`-f` の追従ストリームとは併用できません）
- 別名 `log`

複数サーバーのログを対象を切り替えながら読んだり、絞り込み・スクロールしながら
追従したりするには、対話的な [ターミナル UI](tui.md)（`wt ui`）が便利です。

## 状態とログの保存先

状態は `$XDG_STATE_HOME/worktree-integrator/`（未設定時は
`~/.local/state/worktree-integrator/`）に保存されます:

- `servers.toml` — サーバーごとの稼働記録（PID・active worktree・初期化済み一覧）
- `aliases.toml` — worktree の別名
- `logs/<repo>__<server>__<worktree>.log` — サーバーの標準出力／標準エラー。起動の
  たびに既存ログは `.log.prev` へローテーションされます（1 世代保持）

## 別名（`alias` サブコマンド） {#alias}

worktree に人間向けの表示名を付けられます。`wt list` と `server status` の ALIAS 列に
表示されます。たとえば worktree 名 `ABC-123` に Jira チケットのタイトルを登録して
おくと、どの worktree が何の作業か一目で分かります。

```sh
wt alias set <worktree名> <表示名>   # 設定・更新
wt alias list [--json]               # 一覧（別名 ls）
wt alias remove <worktree名>         # 削除（別名 rm）
```

- 表示名は先頭 1 行・前後の空白を除いた形に正規化されます。空の表示名はエラーです
  （消すには `alias remove` を使います）。
- worktree 名の規則は `create` と同じです（[Usage](usage.md) を参照）。
- フックから登録すると便利です。`before` フックで Jira のタイトルを取得して
  `wt alias set "$WT_WORKTREE_NAME" "<タイトル>"` を呼ぶ実例が
  [`examples/hooks/cmux-jira-title.sh`](https://github.com/vimrak-hal/worktree-integrator/blob/main/examples/hooks/cmux-jira-title.sh)
  にあります。
