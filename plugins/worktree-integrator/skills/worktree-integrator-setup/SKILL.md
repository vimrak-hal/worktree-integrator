---
name: worktree-integrator-setup
description: >-
  worktree-integrator を一から初期セットアップする手順。バイナリのビルド／インストール、
  MCP サーバー登録（claude mcp add / .mcp.json）、そして肝心の設定ファイル
  ~/.config/worktree-integrator/config.toml（repos_dir / worktrees_dir / remote・
  base ブランチ・dev サーバー定義 [repos.<name>.servers.<server>]・hooks・copy）の
  作成と、config check / doctor による検証までを案内する。MCP の server_* ツールが
  何も起きない／「サーバー設定がありません」と出る、初めて導入する、dev サーバーを
  切り替えたいのに未設定、設定ファイルがパースエラーで丸ごと無視される、
  といったときに使う。
---

# worktree-integrator 初期セットアップ

このスキルは worktree-integrator を実運用できる状態まで立ち上げるための手順書です。

**なぜスキルが必要か**: dev サーバー管理（`server switch/status/stop/logs`、MCP の
`server_*` ツール）は、設定ファイル `~/.config/worktree-integrator/config.toml` の
`[repos.<name>.servers.<server>]` で宣言したサーバーにしか作用しません。未設定だと黙って
no-op になり、CLI / MCP のどちらにも「この設定ファイルを作る」操作は存在しません
（`worktree_create` の hooks / copy / base も同じ設定ファイル由来）。つまり
**設定ファイルの作成は人手（このスキル）でやるしかない**初期セットアップの中核です。

## このスキルの進め方（Claude 向け）

1. 下の各ステップを順に進める。**パスやコマンドを勝手に推測しない** — リポジトリの場所、
   dev サーバーの起動コマンドなどは必ずユーザーに確認する。
2. 設定ファイルを書く前に、既存の `~/.config/worktree-integrator/config.toml` の有無を
   確認する。既にあれば中身を読み、上書きせず追記・マージする（消す前に必ず提示して確認）。
   なお `~/.config/worktree-integrator.toml`（拡張子直付け、ディレクトリではない）という
   旧パスは読み込まれない死んだファイルなので、見つけても移行対象として扱う。
3. キー名は厳密一致が必要（未知キーはパースエラーになり、**設定全体が読み込まれない**）。
   **設定ファイルを書き換えるたびに `worktree-integrator config check` を実行**し、
   exit 0 を確認してから次へ進む。
4. 最後に必ずステップ 4 の検証を実行し、結果をユーザーに見せる。
5. 「セットアップしたはずなのに動かない」という相談なら、先に同梱の読み取り専用診断
   エージェント **worktree-integrator-doctor** に現状調査を任せ、その報告をもとに
   このスキルの該当ステップへ戻るのが早い。

詳細リファレンス（必要になったときに読む）:

- [references/config-reference.md](references/config-reference.md) —
  config.toml スキーマ v2 の全キー（base 解決順・copy のマージ規則・hooks の環境変数まで）
- [references/troubleshooting.md](references/troubleshooting.md) —
  症状→原因→対処の一覧と、`doctor` の全チェック対応表

---

## ステップ 1: バイナリを用意する

Go（1.25 以降）で実装されている。C コンパイラや CMake は不要（Git 操作はローカルの
`git` コマンドに委譲するため、libgit2 等のネイティブ依存もない）。

```sh
# インストール（推奨・$GOPATH/bin に配置される）
go install github.com/vimrak-hal/worktree-integrator/cmd/worktree-integrator@latest

# もしくはリポジトリ直下でビルド
go build -o worktree-integrator ./cmd/worktree-integrator
```

以降の手順では絶対パスが要るので、配置先を決める。PATH に通すなら `~/.local/bin/`
などへシンボリックリンクし、`worktree-integrator --version` が通ることを確認する。
長いのでドキュメントでは `wt` へのリンクを併用している（必須ではない）:

```sh
ln -s "$(command -v worktree-integrator)" ~/.local/bin/wt
```

> `@latest` は git タグをブランチ最新コミットより優先することがある。バージョンが
> 期待とずれる（想定より古い）場合は `go version -m $(command -v worktree-integrator)` で
> 解決されたモジュールバージョンを確認し、疑わしければリポジトリ直下で `go build` して
> 直接ソースからビルドした方を使う。

## ステップ 2: MCP サーバーとして登録する（任意・推奨）

CLI と同じワークフローを MCP ツールとして Claude から呼べるようにする。

```sh
# 自分の環境だけ（user スコープ、全プロジェクトから利用可）
claude mcp add worktree-integrator --scope user -- /絶対パス/worktree-integrator mcp
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

登録後 `claude mcp get worktree-integrator` で `✔ Connected` になることを確認する。

利用できるツール（11 個、名前は noun_verb で統一）:

| ツール | 対応する CLI |
| --- | --- |
| `repos_list` | `repos` |
| `worktree_create` / `worktree_list` / `worktree_remove` | `create` / `list` / `remove` |
| `server_switch` / `server_status` / `server_stop` / `server_logs` | `server *` |
| `alias_set` / `alias_list` / `alias_remove` | `alias *` |

**この時点ではまだ `server_*` は no-op** — 次のステップで設定する。

> 注意 2 点: (1) ツール一覧は**セッション開始時にしか読み込まれない**ので、バイナリを
> 再ビルド／再配置したら Claude Code を再起動（または `/mcp` から再接続）する。
> (2) セッションに `list_repos` / `create_worktrees` / `alias_get` という**旧名称**の
> ツールが見えている場合、接続先のバイナリが古い。ステップ 1 からやり直して再接続する。

## ステップ 3: 設定ファイルを作成する（中核）

場所は固定で `$XDG_CONFIG_HOME/worktree-integrator/config.toml`
（通常 `~/.config/worktree-integrator/config.toml`）。ファイルが無ければ組み込み既定値が
使われる。すべてのキーは任意で、書かなかったキーは次の優先順位でフォールバックする:

```
コマンドラインフラグ / MCP パラメータ ＞ 環境変数 ＞ 設定ファイル ＞ 組み込み既定値
```

> 設定ファイルは未知キーを拒否する。トップレベルに置けるのは `repos_dir` /
> `worktrees_dir` / `remote` / `concurrency` と、`[defaults]` / `[[hooks.*]]` /
> `[repos.<name>]` のみ。綴り違いはパースエラーになり、その時点で**設定全体が
> 読み込まれない**。書いたら必ず `worktree-integrator config check` で確認する
> （正常なら exit 0、不正なら検証エラーを表示して exit 1）。

### 3a. ディレクトリ・remote・並列数（任意）

| キー | 既定値 | 説明 |
| --- | --- | --- |
| `repos_dir` | `~/repositories` | 対象リポジトリ（クローン元）のベースディレクトリ（`~` は展開される） |
| `worktrees_dir` | `~/worktrees` | worktree 作成先（`<worktrees_dir>/<worktree名>/<repo>/`） |
| `remote` | `origin` | fetch 対象の remote |
| `concurrency` | 自動決定 | worktree 作成の並列数。CLI の `-j` / MCP の `concurrency` でも上書き可 |

```toml
# ~/.config/worktree-integrator/config.toml
repos_dir     = "/srv/repos"
worktrees_dir = "/srv/worktrees"
remote        = "upstream"
```

- 既定で問題なければこの節は丸ごと省略してよい。
- ディレクトリは環境変数 `WT_REPOS_DIR` / `WT_WORKTREES_DIR` でも上書きできる。

ユーザーに確認すること: リポジトリ群がどのディレクトリにあるか、worktree をどこに
作りたいか、remote 名（`origin` 以外を使うか）。

### 3b. ベースブランチ `base`（任意）

worktree の作成元ブランチは次の順で解決される:

```
--base フラグ / MCP の base ＞ [repos.<name>].base ＞ [defaults].base ＞ "auto"
```

`auto`（既定）はリモートのデフォルトブランチ（`refs/remotes/<remote>/HEAD`）→
`main` → `master` の順にローカルの ref だけで解決する。`develop` 起点のリポジトリが
あるなど既定で困る場合だけ書く:

```toml
[defaults]
base = "main"          # 全リポジトリ共通の既定

[repos.api]
base = "develop"       # このリポジトリだけ上書き
```

### 3c. dev サーバー定義 `[repos.<name>.servers.<server>]`

`server_*` を機能させる本体。`<name>` は **`repos_dir` 直下のディレクトリ名**、
`<server>` は任意の名前（1 リポジトリに複数定義可、例: `backend` / `frontend`）。

| キー | 必須/既定 | 説明 |
| --- | --- | --- |
| `start` | **必須** | 常駐サーバーの起動コマンド。detach（独立セッション）で起動し、CLI/MCP 終了後も動き続ける |
| `dir` | 任意 | サーバーを動かす worktree 内のサブディレクトリ。未指定なら worktree ルート |
| `setup` | 任意 | その worktree を**初めて**有効化したときだけ実行（依存インストール・DB 初期化など） |
| `on_activate` | 任意 | **毎回**（初回・切替の両方）、`start` の直前に実行 |
| `on_switch` | 任意 | **初期化済みの worktree に切り替えたときだけ**実行 |
| `stop_grace_secs` | `5` | 停止時に `SIGTERM` → `SIGKILL` へ昇格するまでの猶予（秒） |

各コマンドは**単一の文字列**（`&&` やパイプも可）か**文字列の配列**（逐次実行）で書ける。
実行順は、初回有効化が `setup` → `on_activate` → `start`、切替（初期化済み）が
`on_switch` → `on_activate` → `start`（`start` は最後に detach 起動）。作業ディレクトリは
`<worktree>/<dir>`。hooks と同じ `WT_*` 環境変数に加え `WT_SERVER_NAME` が渡される
（一覧は [config-reference](references/config-reference.md)）。

```toml
[repos.rails-tutorial.servers.backend]
start       = "bin/rails server -p 3000"                  # 必須
dir         = "backend"                                   # 任意
setup       = ["bundle install", "bin/rails db:migrate"]  # 初回だけ
on_activate = "lsof -ti:3000 | xargs -r kill"             # 毎回 / start 直前
on_switch   = "bin/rails db:migrate"                      # 切替時のみ
stop_grace_secs = 10

[repos.rails-tutorial.servers.frontend]
start = "npm run dev"
dir   = "frontend"
setup = ["npm ci"]
```

ユーザーに確認すること: どのリポジトリに dev サーバーがあるか、起動コマンド、
サブディレクトリの有無、初回だけ流したいセットアップ（依存インストール等）。

### 3d. hooks（任意）

worktree 作成ワークフローの各タイミング（`before` / `after_worktree` / `after`）で
任意のシェルコマンドを実行する。`[[hooks.<timing>]]` の配列テーブルで書く。
代表例:

```toml
[[hooks.after_worktree]]
name    = "setup"
command = ["npm ci", "npm run build"]
```

> `background`（旧キー）は**廃止済みで、指定するとパースエラーになる**。常駐プロセスは
> hooks ではなく `[repos.<name>.servers.<server>]`（3c）で管理する。

hooks の設計（タイミング選択・`WT_*` 環境変数・失敗方針・timeout・既存設定への
マージ・検証）は、同梱の専門エージェント **worktree-integrator-hooks** に任せるのが
早い。キー一覧が必要なだけなら [config-reference](references/config-reference.md) の
`[[hooks.<timing>]]` 節を参照する。すぐ使えるサンプルは本体リポジトリの
`examples/hooks/`（`cmux-jira-title.sh` / `cmux-open-worktree.sh`）にある。

### 3e. 追加ファイルのコピー `[defaults.copy]` / `[repos.<name>.copy]`（任意）

`.env` など git 管理外だが worktree に引き継ぎたいファイルの指定。

| キー | 既定 | 説明 |
| --- | --- | --- |
| `paths` | `[]` | 常にコピーする明示的な相対パスのリスト |
| `gitignored` | `false` | `true` で gitignore された未追跡ファイルを丸ごとコピー対象にする |
| `exclude` | `[]` | gitignored モードでの追加除外パターン（gitignore 互換） |
| `exclude_defaults` | `true` | 組み込み既定除外（`node_modules` / `.venv` / `venv` / `target` / `.direnv` / `.cache` / `.DS_Store`）を適用するか |

`[defaults.copy]` が全リポジトリ共通、`[repos.<name>.copy]` がリポジトリ別の上書き
（マージ規則は [config-reference](references/config-reference.md)）。パスのリストだけ
なら `copy = [".env"]` という速記形も使える。

```toml
[defaults.copy]
gitignored = true

[repos.api.copy]
paths   = ["backend/.env"]
exclude = ["*.local.log"]
```

## ステップ 4: 検証する

上から順に実行し、結果をユーザーに見せる:

1. **設定が構文的に正しいか**: `worktree-integrator config check`（exit 0 を確認。
   不正なら検証エラーが表示され exit 1）。
2. **リポジトリが見えるか**: `worktree-integrator repos`（MCP なら `repos_list`）に
   対象リポジトリが出るか。出ない場合は `repos_dir` を見直す。
3. **自己診断**: `worktree-integrator doctor`。`config_without_repo`（設定に居るのに
   実在しない = `<name>` のタイプミス検出）と `repo_without_servers`（サーバー設定が
   ないリポジトリの可視化）が特にセットアップ確認に効く。全チェックの意味は
   [troubleshooting](references/troubleshooting.md)。
4. **サーバー定義が見えるか**: `worktree-integrator server status`（MCP の
   `server_status`）。「サーバー設定がありません」と出る場合、`<name>` が `repos_dir`
   直下のディレクトリ名と一致しているか確認する。
5. **実際に切り替える**: worktree を作ってから `server switch <名前>`（MCP の
   `server_switch`）→ `server status` で稼働中と PID を確認。ログは
   `server logs <名前>`（MCP の `server_logs` は追従せず末尾行のみ）。

> 状態とログの保存先（参考）: active worktree と PID は
> `~/.local/state/worktree-integrator/servers.toml`（`$XDG_STATE_HOME` 優先）、別名は
> 同ディレクトリの `aliases.toml`、サーバー出力は `logs/<repo>__<server>__<worktree>.log`
> （起動のたび 1 世代前が `.log.prev` へローテーション）。自動生成されるので手で作る
> 必要はない。

## 完成形の例（フルセット）

```toml
# ~/.config/worktree-integrator/config.toml

# --- ディレクトリ / remote（既定でよければ省略可）---
repos_dir     = "/srv/repos"
worktrees_dir = "/srv/worktrees"
remote        = "origin"

# --- 全リポジトリ共通の既定値 ---
[defaults]
base = "main"

[defaults.copy]
gitignored = true

# --- dev サーバー（server_* の対象）---
[repos.rails-tutorial.servers.backend]
start       = "bin/rails server -p 3000"
dir         = "backend"
setup       = ["bundle install", "bin/rails db:migrate"]
on_activate = "lsof -ti:3000 | xargs -r kill"
on_switch   = "bin/rails db:migrate"
stop_grace_secs = 10

[repos.rails-tutorial.servers.frontend]
start = "npm run dev"
dir   = "frontend"
setup = ["npm ci"]

# --- リポジトリ別の上書き ---
[repos.api]
base = "develop"

[repos.api.copy]
paths = ["backend/.env"]

# --- hooks（任意）---
[[hooks.after_worktree]]
name    = "install"
command = ["npm ci"]

[[hooks.after]]
name          = "notify"
command       = "notify-send \"worktree $WT_WORKTREE_NAME 作成完了\""
allow_failure = true
```

## よくある詰まりどころ（要約）

| 症状 | 原因と一次対処 |
| --- | --- |
| `server_*` が no-op /「サーバー設定がありません」 | `[repos.<name>.servers.*]` 未定義、または `<name>` が `repos_dir` 直下のディレクトリ名と不一致（3c） |
| 設定がまるごと無視される | 未知キー・綴り違いでパースエラー。`config check` で確認（既定値フォールバックではなく**エラーで停止**する） |
| MCP に `list_repos` / `create_worktrees` / `alias_get` が見える | 接続先バイナリが古い。再ビルドして Claude Code を再起動 |
| 再ビルドしたのにツールが変わらない | ツール一覧はセッション開始時に固定。Claude Code を再起動（`/mcp` から再接続） |
| `rm -rf` した worktree の残骸・古い記録 | `worktree-integrator doctor --fix` で一括掃除 |
| 旧設定ファイル・旧スキーマの残骸 | `~/.config/worktree-integrator.toml`（拡張子直付け）や `[servers.<repo>.<server>]` / トップレベル `[copy]` / hooks の `background` は旧版の名残。新パス・新スキーマへ移行する |

より詳しい症状別の切り分けは [references/troubleshooting.md](references/troubleshooting.md)、
環境全体の一次調査は **worktree-integrator-doctor** エージェント、hooks の設計・追加・
修正は **worktree-integrator-hooks** エージェントに任せられる。
