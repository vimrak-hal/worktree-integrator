# 使い方

```sh
wt <name>                # ≡ wt create <name>
wt create <name> [--repo <repo>]... [--all] [--base <ref>] [-j N]
wt list   [--json]
wt enter  <name>
wt remove <name> [--force] [--keep-branch]
wt repos
wt server switch <name> [--repo <repo>]... [--require-worktree] [--restart]
wt server status [--repo <repo>]... [--json]
wt server stop   [<name>] [--repo <repo>]...
wt server logs   [<name>] [--repo <repo>]... [-n N] [-f] [--prev]
wt alias set <name> <label> | wt alias list | wt alias rm <name>
wt doctor [--fix] [--json]
wt config check
wt mcp
```

素の `wt <name>` は `wt create <name>` と等価です。先頭の位置引数が既存のサブコマンド名
（`create` / `list` / `enter` / `remove` / `doctor` / `repos` / `server` / `alias` / `mcp` /
`config` / `help`）と衝突する場合は、worktree 名としては扱わず「`wt create <name>` と明示して
ください」というエラーになります（黙って別の動作にすり替わることはありません）。

## `create` — worktree の作成（冪等・差分）

```sh
wt create <name> [--repo <repo>]... [--all] [--base <ref>] [-j N]
```

1. `before` フックが常に実行されます（対象の有無によらず）。
2. リポジトリディレクトリ（既定 `~/repositories/`）直下の Git リポジトリを探索します。
3. 対象リポジトリを決めます:
   - `--repo <repo>`（繰り返し指定可）: 明示したリポジトリだけが対象。**存在しない名前は
     エラー**になります。
   - `--all`: 探索されたすべてのリポジトリが対象。
   - どちらも省略、かつ対話プロンプトが使える（標準入出力が端末）場合: チェックボックスの
     対話選択に入ります。**この worktree にまだ存在しないリポジトリだけ**が選択肢に
     並びます（差分作成）。探索されたリポジトリがあるのに候補が 1 つも無い場合（＝全リポジトリが
     作成済み）は「追加するリポジトリはありません」と表示して正常終了します（`after` フックは
     実行されます）。
   - どちらも省略、かつ非対話（パイプ・CI・MCP 等）の場合: 「--repo か --all を指定して
     ください」というエラーになります。
   - `--repo` と `--all` を同時に指定するとエラーです。
4. 選択された各リポジトリについて、ベースブランチ（`--base` / `[repos.<repo>].base` /
   `[defaults].base` / 自動検出。[Configuration](configuration.md) を参照）を fetch し、
   `<worktrees_dir>/<name>/<repo>/` に `<name>` という名前のブランチで worktree を並列に
   作成します。
5. 新規作成された各リポジトリに、設定された追加ファイルをコピーします（[Copy Files](copy-files.md)）。
6. `after_worktree` フックを新規作成された worktree ごとに、`after` フックを全体で 1 回実行します。

**冪等性**: 同じ `<name>` で再実行しても、既に worktree のメンバーとして存在するリポジトリは
壊れません。`--repo` / `--all` 指定時、既存メンバーは "スキップ" として報告されるだけで
エラーにはなりません（`before` フックは毎回実行されます）。詳細な判定ルールは
[Behavior](behavior.md) を参照してください。

## `list` — worktree の一覧

```sh
wt list [--json]
```

`worktrees_dir` を実スキャンし、各 worktree について所属リポジトリ（ブランチ・健全性）・
別名・現在稼働中のサーバーを 1 つの表に統合して表示します。

```
WORKTREE   ALIAS               REPOS       SERVERS
ABC-123    ログイン画面の修正    api, web    api/backend: 稼働中 (pid 4242)
fix-crash  -                   api         -
(!) old-x  -                   web（壊れた gitdir） ← doctor --fix で修復
```

壊れたチェックアウト（ソースリポジトリを削除・作り直した後などに残る、実体を失った
gitdir ポインタ）を含む worktree には `(!)` が付き、`doctor --fix` での修復が案内されます。

## `enter` — 既存 worktree への遷移

```sh
wt enter <name>
```

`<name>` の worktree ルートが存在することを確認し、**`after` フックだけ**を実行します。
新しくリポジトリを追加したり作り直したりはしません。cmux などへの自動遷移フックを
「ただそこへ移動するだけ」の用途で使いたい場合はこのコマンドを使います。旧仕様
（`create` が既存の worktree ルートを検出すると全処理をスキップして `after` フックだけを
実行していたショートサーキット）からの移行については [Behavior](behavior.md) を参照してください。

## `remove` — worktree の削除

```sh
wt remove <name> [--force] [--keep-branch]
```

以下を順に行います:

1. その worktree で稼働中のサーバーをすべて停止します（失敗したらここで中断し、以降の
   手順には進みません — 稼働中のプロセスを残したままチェックアウトやログを消さないため）。
2. 各メンバーリポジトリを `git worktree remove` で削除します。未コミットの変更があると
   git が拒否します（`--force` で上書き）。`--keep-branch` を指定しない限り、続けて
   `git branch -D <name>` でブランチも削除します。
3. 全サーバーの setup 記録からこの worktree 名を取り除きます（同名で作り直したときに
   setup が再実行されるようにするため）。
4. 別名を削除します。
5. この worktree のログ（`.prev` 世代を含む）を削除します。
6. worktree ルートディレクトリを削除します（メンバーの削除がすべて成功した場合のみ。
   1 件でも失敗して残ったチェックアウトを巻き込んで `rm -rf` することはありません）。

途中の失敗は結果に集約され、進められるところまで進めたうえで最後にエラーを返します。

## `doctor` — 自己診断・自己修復

```sh
wt doctor [--fix] [--json]
```

8 種類のチェックを行い、発見（Finding）を一覧します。`--fix` を付けると修復可能な発見を
その場で修復します（付けない場合は報告のみで何も変更しません）。発見があること自体は
エラー扱いではありません（exit 0）。

| チェック | 内容 | `--fix` の動作 |
| --- | --- | --- |
| `dead_running` | 稼働記録があるが実プロセスが消滅している | 記録をクリア |
| `stale_setup` | setup 記録がある worktree が実在しない | 記録を削除 |
| `stale_alias` | 別名が設定された worktree が実在しない | 別名を削除 |
| `orphan_log` | どこからも参照されないログファイル | ファイルを削除 |
| `prunable_worktrees` | `git worktree prune` で掃除できる残骸がある | `git worktree prune` を実行 |
| `broken_repo` | `repos_dir` のエントリの `.git` が無効（名ばかりリポジトリ） | 報告のみ |
| `config_without_repo` | 設定 `[repos.<name>]` があるが `repos_dir` に実在しない | 報告のみ |
| `repo_without_servers` | `repos_dir` に実在するがサーバー設定が無い | 報告のみ |

`rm -rf` で worktree を手動削除した場合の残骸（stale_setup / stale_alias / orphan_log /
prunable_worktrees）は、このコマンドで一括して片付けられます。

## `repos` — 探索されたリポジトリの一覧

```sh
wt repos
```

`repos_dir` 直下で Git リポジトリとして検出されたディレクトリの一覧を表示します
（`create --repo` に指定できる名前の一覧でもあります）。

## `server` — dev サーバーの管理

`server switch/status/stop/logs` の詳細は [Server Management](server-management.md) を参照して
ください。

## `alias` — worktree の表示名

`alias set/list/rm` の詳細は [Alias](alias.md) を参照してください。

## `config check` — 設定ファイルの検証

```sh
wt config check
```

設定ファイルを既定パスから読み込み、スキーマと必須フィールドを検証します。

- ファイルが無ければ「設定ファイルがありません（`<path>`）。既定値で動作します」と表示して
  **exit 0**。
- 存在し正常なら「設定は正常です（`<path>`）」と表示して **exit 0**。
- 存在するが不正なら検証エラーを標準エラーへ出力して **exit 1**。

CI で設定ファイルの妥当性だけを検査したい場合に使えます。

## `mcp` — MCP サーバーとして起動

```sh
wt mcp
```

詳細は [MCP Server](mcp-server.md) を参照してください。

## フラグ / 環境変数

`--repos-dir` / `--worktrees-dir` / `--remote` / `-j`（`--concurrency`）は **`create` の
ローカルフラグ**です。`--repos-dir` / `--worktrees-dir` は `server` とそのサブコマンド
（`switch` / `status` / `stop` / `logs`）でも受け付けます。`list` / `enter` / `remove` /
`doctor` / `repos` / `alias` にはこれらのフラグは無く、設定ファイルと環境変数だけから
ディレクトリを解決します（黙って無視されるのではなく、フラグ自体が存在しません）。

| フラグ | 環境変数 | 既定値 | 対応コマンド |
| --- | --- | --- | --- |
| `--repos-dir` | `WT_REPOS_DIR` | `~/repositories` | `create` / `server *` |
| `--worktrees-dir` | `WT_WORKTREES_DIR` | `~/worktrees` | `create` / `server *` |
| `--remote` | `WT_REMOTE` | `origin` | `create` |
| `--concurrency`, `-j` | `WT_CONCURRENCY` | 自動（[Behavior](behavior.md) を参照） | `create` |
| `--base` | – | `auto`（[Configuration](configuration.md) を参照） | `create` |

優先順位はすべての項目で共通です（例外なし）:

```
コマンドラインフラグ ＞ 環境変数 ＞ 設定ファイル ＞ 組み込みの既定値
```

## 終了コード

| コード | 意味 |
| --- | --- |
| `0` | 成功 |
| `1` | エラー |
| `130` | キャンセル（Ctrl-C / SIGTERM、または対話プロンプトの中断） |

Ctrl-C はシグナルとして受け取られ、実行中の git・フック・サーバーのライフサイクル
コマンドまで `context.Context` のキャンセルが伝播します（デタッチ起動されたサーバー本体は
仕様として対象外です）。対話選択プロンプトの中断（Ctrl-C）も、空選択（何も選ばず Enter）とは
区別されて exit 130 になります。
