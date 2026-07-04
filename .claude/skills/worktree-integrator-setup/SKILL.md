---
name: worktree-integrator-setup
description: >-
  worktree-integrator を一から初期セットアップする手順。バイナリのビルド／配置、MCP
  サーバー登録（claude mcp add / .mcp.json）、そして肝心の設定ファイル
  ~/.config/worktree-integrator.toml（repos_dir / worktrees_dir / remote・dev サーバー
  定義 [servers.<repo>.<server>]・hooks）の作成と検証までを案内する。MCP の server_*
  ツールが何も起きない／「サーバー設定がありません（[servers.*] を設定してください）」と
  出る、初めて導入する、dev サーバーを切り替えたいのに未設定、といったときに使う。
---

# worktree-integrator 初期セットアップ

このスキルは worktree-integrator を実運用できる状態まで立ち上げるための手順書です。

**なぜスキルが必要か**: dev サーバー管理（`server switch/status/stop/logs`、MCP の
`server_*` ツール）は、設定ファイル `~/.config/worktree-integrator.toml` の
`[servers.<repo>.<server>]` で宣言したサーバーにしか作用しません。未設定だと黙って
no-op になり、CLI / MCP のどちらにも「この設定ファイルを作る」操作は存在しません
（`create_worktrees` の hooks も同じ設定ファイル由来）。つまり**設定ファイルの作成は
人手（このスキル）でやるしかない**初期セットアップの中核です。

## このスキルの進め方（Claude 向け）

1. 下の各ステップを順に進める。**パスやコマンドを勝手に推測しない** — リポジトリの場所、
   dev サーバーの起動コマンドなどは必ずユーザーに確認する。
2. 設定ファイルを書く前に、既存の `~/.config/worktree-integrator.toml` の有無を確認する。
   既にあれば中身を読み、上書きせず追記・マージする（消す前に必ず提示して確認）。
3. キー名は厳密一致が必要（後述のとおり未知キーはパースエラーになる）。例の構造から
   逸脱しない。
4. 書き込んだら必ず検証ステップを実行し、結果をユーザーに見せる。

---

## ステップ 1: バイナリをビルド／配置する

```sh
cargo build --release
# 生成物: target/release/worktree-integrator
```

- ビルドには **C コンパイラと CMake** が必要（`git2` クレートが libgit2 を vendored
  ビルドするため。システムに libgit2 があればそちらにリンク）。
- 以降の手順では絶対パスが要るので、配置先を決める。PATH に通すなら
  `~/.local/bin/` などへコピー／シンボリックリンクし、`worktree-integrator --help` が
  通ることを確認する。

## ステップ 2: MCP サーバーとして登録する（任意・推奨）

CLI と同じワークフローを MCP ツールとして Claude から呼べるようにする。

```sh
# 自分の環境だけ（local スコープ）
claude mcp add worktree-integrator -- /絶対パス/worktree-integrator mcp
```

リポジトリ単位で共有したい場合はプロジェクト直下に `.mcp.json` を置く:

```json
{
  "mcpServers": {
    "worktree-integrator": {
      "command": "/絶対パス/worktree-integrator",
      "args": ["mcp"]
    }
  }
}
```

登録後 `/mcp` でツール（`list_repos` / `create_worktrees` / `server_*` / `alias_*`）が
見えることを確認する。**この時点ではまだ `server_*` は no-op** — 次のステップで設定する。

## ステップ 3: 設定ファイルを作成する（中核）

場所は固定で `~/.config/worktree-integrator.toml`。ファイルが無ければ組み込み既定値が
使われる。すべてのキーは任意で、書かなかったキーは次の優先順位でフォールバックする:

```
コマンドラインフラグ / MCP パラメータ ＞ 環境変数 ＞ 設定ファイル ＞ 組み込み既定値
```

> 設定ファイルは未知キーを拒否する（`deny_unknown_fields`）。トップレベルに置けるのは
> `repos_dir` / `worktrees_dir` / `remote` と、`[[hooks.*]]` / `[servers.*]` のみ。
> 綴り違いはパースエラーになり、その時点で**設定全体が読み込まれない**点に注意。

### 3a. ディレクトリと remote（任意）

| キー | 既定値 | 説明 |
| --- | --- | --- |
| `repos_dir` | `~/repositories` | 対象リポジトリ（クローン元）のベースディレクトリ |
| `worktrees_dir` | `~/worktrees` | worktree 作成先のベースディレクトリ（`<worktrees_dir>/<worktree名>/<repo>/`） |
| `remote` | `origin` | fetch 対象の remote |

```toml
# ~/.config/worktree-integrator.toml
repos_dir     = "/srv/repos"
worktrees_dir = "/srv/worktrees"
remote        = "upstream"
```

- 既定（`~/repositories` / `~/worktrees` / `origin`）で問題なければ、この節は丸ごと
  省略してよい。
- ディレクトリは環境変数 `WT_REPOS_DIR` / `WT_WORKTREES_DIR` でも上書きできる（remote に
  対応する環境変数は無い）。
- `concurrency`（並列数）は**設定ファイルでは指定できない**。自動決定で、必要時のみ
  CLI の `-j` / MCP の `concurrency` で上書きする。

ユーザーに確認すること: リポジトリ群がどのディレクトリにあるか、worktree をどこに作りたいか、
remote 名（`origin` 以外を使うか）。

### 3b. dev サーバー定義 `[servers.<repo>.<server>]`

`server_*` を機能させる本体。`<repo>` は **`repos_dir` 直下のディレクトリ名**、
`<server>` は任意の名前（1 リポジトリに複数定義可、例: `backend` / `frontend`）。

| キー | 必須/既定 | 説明 |
| --- | --- | --- |
| `start` | **必須** | 常駐サーバーの起動コマンド。detach（独立セッション）で起動し、CLI/MCP 終了後も動き続ける |
| `dir` | 任意 | サーバーを動かす worktree 内のサブディレクトリ。未指定なら worktree ルート |
| `setup` | 任意 | その worktree を**初めて**有効化したときだけ実行（依存インストール・DB 初期化など） |
| `on_activate` | 任意 | **毎回**（初回・切替の両方）、`start` の直前に実行 |
| `on_switch` | 任意 | **初期化済みの worktree に切り替えたときだけ**実行 |
| `stop_grace_secs` | `5` | 停止時に `SIGTERM` → `SIGKILL` へ昇格するまでの猶予（秒） |

`start` / `setup` / `on_activate` / `on_switch` のコマンドは、**単一の文字列**（`&&` や
パイプも可）か、**文字列の配列**（記述順に逐次実行＝内部的に `&&` 連結）で書ける。

実行順（`start` は最後に detach 起動）:

- **初回有効化**: `setup` → `on_activate` → `start`
- **切替（初期化済み）**: `on_switch` → `on_activate` → `start`

作業ディレクトリは `<worktree>/<dir>`。各コマンドには hooks と同じ `WT_*` 環境変数
（`WT_WORKTREE_NAME` / `WT_REPO_NAME` / `WT_REPO_PATH` / `WT_WORKTREE_PATH` / `WT_ROOT`）に
加え、`WT_SERVER_NAME` が渡される。

```toml
[servers.rails-tutorial.backend]
start       = "bin/rails server -p 3000"                  # 必須
dir         = "backend"                                   # 任意
setup       = ["bundle install", "bin/rails db:migrate"]  # 初回だけ
on_activate = "lsof -ti:3000 | xargs -r kill"             # 毎回 / start 直前
on_switch   = "bin/rails db:migrate"                      # 切替時のみ
stop_grace_secs = 10

[servers.rails-tutorial.frontend]
start = "npm run dev"
dir   = "frontend"
setup = ["npm ci"]
```

ユーザーに確認すること: どのリポジトリに dev サーバーがあるか、起動コマンド、
サブディレクトリの有無、初回だけ流したいセットアップ（依存インストール等）。

### 3c. hooks（任意）

worktree 作成ワークフローの各タイミングで任意のシェルコマンドを実行する。同じ
タイミングの hooks は**並列**実行。`[[hooks.<timing>]]` の配列テーブルで書く。

タイミング: `before`（全体で1回・リポジトリ探索前）/ `after_worktree`（作成された
worktree ごとに1回・その worktree 内）/ `after`（全体で1回・全処理後）。

| キー | 必須/既定 | 説明 |
| --- | --- | --- |
| `name` | **必須** | 進捗・サマリ表示名 |
| `command` | **必須** | `sh -c` で解釈。単一文字列か文字列配列（逐次実行） |
| `background` | `false` | `true` で完了を待たず起動だけ（常駐プロセス向け） |
| `allow_failure` | `false` | `true` で失敗しても全体は失敗扱いにせず警告どまり |
| `workdir` | 任意 | 作業ディレクトリの明示指定。未指定時は各タイミングの既定 |

```toml
[[hooks.after_worktree]]
name    = "setup"
command = ["npm ci", "npm run build"]

[[hooks.after_worktree]]
name       = "dev-server"
command    = "npm run dev > dev-server.log 2>&1"
background = true

[[hooks.after]]
name          = "notify"
command       = "notify-send \"worktree $WT_WORKTREE_NAME 作成完了\""
allow_failure = true
```

> `dev` サーバーの起動は **hooks の `after_worktree`（`background = true`）でも、
> `[servers.*]` でも**実現できる。違いは: hooks は **worktree 作成時に一度起動するだけ**で
> 状態管理なし。`[servers.*]` は **worktree 間の切替・停止・状態/ログ照会**（`server_*`）が
> でき、`server status` に出る。「作って終わり」なら hooks、「切り替えて使い続ける」なら
> `[servers.*]` を勧める。
>
> `before` hook の失敗（`allow_failure` 未指定）はリポジトリ処理前に中断する。
> `after_worktree` / `after` の失敗は処理を続けつつ終了コードを非ゼロにする。

すぐ使える `before` hook のサンプルは `examples/hooks/`（例: `cmux-jira-title.sh`）にある。

## ステップ 4: 検証する

1. **リポジトリが見えるか**: MCP の `list_repos`、または `worktree-integrator <名前>` の
   探索一覧に対象リポジトリが出るか。出ない場合は `repos_dir` を見直す。
2. **設定が読めているか / サーバー定義が見えるか**: `server status`（MCP の `server_status`）。
   設定ミスがあれば「サーバー設定がありません（[servers.*] を設定してください）」や
   パースエラーが出るので、その場で直す。`[servers.*]` を書いたのにこの no-op が出る場合、
   `<repo>` 名が `repos_dir` 直下のディレクトリ名と一致しているか確認する。
3. **実際に切り替える**: worktree を作ってから `server switch <名前>`（MCP の `server_switch`）→
   `server status` で `稼働中` と PID を確認。ログは `server logs <名前>`。

> 状態とログの保存先（参考）: active worktree と PID は
> `~/.local/state/worktree-integrator/servers.toml`（`$XDG_STATE_HOME` 優先）、別名は
> 同ディレクトリの `aliases.toml`、サーバー出力は `logs/<repo>__<server>__<worktree>.log`。
> これらは自動生成されるので手で作る必要はない。

## 完成形の例（フルセット）

```toml
# ~/.config/worktree-integrator.toml

# --- ディレクトリ / remote（既定でよければ省略可）---
repos_dir     = "/srv/repos"
worktrees_dir = "/srv/worktrees"
remote        = "origin"

# --- dev サーバー（server_* の対象）---
[servers.rails-tutorial.backend]
start       = "bin/rails server -p 3000"
dir         = "backend"
setup       = ["bundle install", "bin/rails db:migrate"]
on_activate = "lsof -ti:3000 | xargs -r kill"
on_switch   = "bin/rails db:migrate"
stop_grace_secs = 10

[servers.rails-tutorial.frontend]
start = "npm run dev"
dir   = "frontend"
setup = ["npm ci"]

# --- hooks（任意）---
[[hooks.after_worktree]]
name    = "install"
command = ["npm ci"]

[[hooks.after]]
name          = "notify"
command       = "notify-send \"worktree $WT_WORKTREE_NAME 作成完了\""
allow_failure = true
```

## よくある詰まりどころ

- **`server_*` が何も起きない / 「サーバー設定がありません」**: `[servers.*]` 未定義、または
  `<repo>` 名が `repos_dir` 直下のディレクトリ名と不一致。
- **設定がまるごと無視される**: 未知キーや綴り違いで TOML パースエラー → 既定値に
  フォールバックではなく**エラーで停止**する。`server status` の出力でエラー文言を確認する。
- **MCP からは対話選択できない**: `create_worktrees` は `repos` を明示指定（候補は
  `list_repos` で取得）。`server_logs` は追従（`-f`）せず末尾行だけ返す。
- **`dir` は worktree 内の相対パス**。絶対パスや `repos_dir` 側のパスではない。
