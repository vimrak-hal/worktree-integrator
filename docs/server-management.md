# サーバー管理（`server` サブコマンド）

サーバーアプリ開発向けに、**どの worktree のサーバーを起動するか**を切り替える機能です。
1 つのリポジトリ内に**複数の名前付きサーバー**（例: rails tutorial app の `backend` と
`frontend`）を設定ファイルで定義でき、`server switch` でそのリポジトリの**全サーバーを
まとめて**起動先 worktree へ切り替えます。**リポジトリごとに active な worktree は常に 1 つ**で、
切り替えると元の worktree のサーバーは停止します。

```sh
worktree-integrator server switch <worktree名> [--repo <repo>]... [--require-worktree] [--restart]
worktree-integrator server status [--repo <repo>]...
worktree-integrator server stop [<worktree名>] [--repo <repo>]...
worktree-integrator server logs [<worktree名>] [--repo <repo>]... [-f] [-n <行数>]
```

| コマンド | 説明 |
| --- | --- |
| `server switch <名前>` （別名 `activate`） | 指定 worktree へ切り替え。既定では、その worktree が存在する設定済み全リポジトリが対象で、**各リポジトリの全サーバーをまとめて**(再)起動します（worktree が無いリポジトリは通知してスキップ、`--require-worktree` でエラーに昇格）。`--repo` で対象リポジトリを絞り込み、`--restart` で既に同一 worktree が稼働中でも再起動します。 |
| `server status` （別名 `ls`） | **リポジトリ・サーバーごと**に、どの worktree が active か・その worktree の**別名（ALIAS）**・各サーバーの状態（稼働中 / 停止 / クラッシュ）・PID を一覧表示します。別名は [Alias](alias.md) サブコマンドで登録します。 |
| `server stop [<名前>]` | 稼働中のサーバーを停止。既定で全停止、`<名前>` で active worktree がその名前のリポジトリのみ、`--repo` でリポジトリを絞り込みます（停止するとそのリポジトリの active は解除）。 |
| `server logs [<名前>]` （別名 `log`） | サーバーのログを表示。`<名前>` 指定時は各サーバーの `logs/<repo>__<server>__<名前>.log`（稼働中・停止後・クラッシュ後いずれも閲覧可）、省略時は現在稼働中サーバーのログ。`-n` で末尾行数（既定 50）、`-f` で `tail -f` 追従します。 |

`worktree-integrator <名前>`（= `create <名前>`）の従来の worktree 作成は変わりません。

## サーバーの設定

設定ファイルに `[servers.<リポジトリ名>.<サーバー名>]` のネストしたテーブルで、リポジトリ
ごとに複数のサーバーを定義します（`<リポジトリ名>` は `~/repositories/` 直下のディレクトリ名）。

```toml
# ~/.config/worktree-integrator.toml

[servers.rails-tutorial.backend]
start       = "bin/rails server -p 3000"            # 必須: 常駐サーバー（detach 起動）
dir         = "backend"                             # worktree 内のサブディレクトリ（任意）
setup       = ["bundle install", "bin/rails db:migrate"]  # 初回だけ
on_activate = "lsof -ti:3000 | xargs -r kill"       # 切替時も（初回含む毎回 / start 直前）
on_switch   = "bin/rails db:migrate"                # 切替時のみ（初期化済み worktree への再有効化）

[servers.rails-tutorial.frontend]
start       = "npm run dev"
dir         = "frontend"
setup       = ["npm ci"]
on_activate = ["npm run codegen"]
# on_switch 省略可
```

| キー | 既定値 | 説明 |
| --- | --- | --- |
| `start` | （必須） | 常駐サーバーの起動コマンド。detach（独立セッション）で起動され、CLI 終了後も動き続けます |
| `dir` | （任意） | サーバーを動かす worktree 内のサブディレクトリ（例: `backend`）。未指定時は worktree のルート |
| `setup` | （任意） | その worktree を**初めて**有効化したときだけ実行（依存インストール・DB 初期化など） |
| `on_activate` | （任意） | **毎回**（初回・切替の両方）、`start` の直前に実行 |
| `on_switch` | （任意） | **初期化済みの worktree に切り替えたときだけ**実行 |
| `stop_grace_secs` | `5` | 停止時に `SIGTERM` 後 `SIGKILL` へエスカレーションするまでの猶予（秒） |

ライフサイクル（`setup` / `on_activate` / `on_switch`）と初期化状態は**サーバーごと**に管理され、
実行順は次のとおりです（`start` は最後に detach 起動）：

- **初回有効化**: `setup` → `on_activate` → `start`
- **切替（初期化済み）**: `on_switch` → `on_activate` → `start`

各コマンドはフックの `command` と同じく**文字列**または**文字列の配列**（`&&` で逐次実行）で
指定できます。失敗するとそのサーバーは起動されず「失敗」として報告され、他サーバー・他
リポジトリの処理は続行します。作業ディレクトリは `<worktree>/<dir>` で、フックと同じ `WT_*`
環境変数（`WT_WORKTREE_NAME` / `WT_REPO_NAME` / `WT_REPO_PATH` / `WT_WORKTREE_PATH` /
`WT_ROOT`）に加え、サーバー名 `WT_SERVER_NAME` が渡されます。

## 状態とログの保存先

各リポジトリの active worktree と、サーバーごとの記録（PID・プロセスグループ・初期化済み
worktree 一覧）は `$XDG_STATE_HOME/worktree-integrator/servers.toml`（未設定時は
`~/.local/state/worktree-integrator/servers.toml`）に保存されます。サーバーの標準出力／
標準エラーは同ディレクトリの `logs/<repo>__<server>__<worktree>.log` に追記されます。状態の
更新は排他ロック下でアトミック（一時ファイル＋ rename）に行われます。

> サーバーは独立したセッション（`setsid`）として起動し、停止時はプロセスグループ全体に
> シグナルを送るため、`npm run dev` のような子プロセスを伴うサーバーも丸ごと停止できます。
