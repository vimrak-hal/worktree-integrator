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
wt alias set <name> <label> | wt alias list | wt alias remove <name>
wt doctor [--fix] [--json]
wt config check
wt mcp
```

## `create` — worktree の作成

```sh
wt create <name> [--repo <repo>]... [--all] [--base <ref>] [-j N]
```

素の `wt <name>` は `wt create <name>` の省略形です（サブコマンド名と同じ worktree
名を使うときだけ `create` を明示します）。

`repos_dir`（既定 `~/repositories/`）直下の Git リポジトリを対象に、
`<worktrees_dir>/<name>/<リポジトリ名>/` へ worktree を並列作成します。この流れの中で
追加ファイルのコピー（[Copy Files](copy-files.md)）と `before` / `after_worktree` /
`after` フック（[Hooks](hooks.md)）も実行されます。

対象リポジトリの選び方は 3 通りです:

- **`--repo <名前>`**（繰り返し可）: 指定したリポジトリだけ。`repos_dir` に無い名前は
  エラーです。
- **`--all`**: 検出された全リポジトリ。
- **どちらも省略**: 端末で対話選択します。この worktree にまだ無いリポジトリだけが
  選択肢に並びます。非対話環境（パイプ・CI）では `--repo` か `--all` が必要です。

`create` は冪等です。同じ `<name>` で再実行すると、既存のリポジトリは壊さずスキップし、
未作成のリポジトリだけを差分で追加します。全リポジトリが作成済みなら「追加するリポジトリは
ありません」と表示して正常終了します（`after` フックは実行されます）。

### worktree 名の規則

worktree 名はブランチ名とディレクトリパスを兼ねるため、`git check-ref-format` に
準拠した規則で検証されます:

- `/` 区切りの階層名が使えます（例: `feature/login`）。
- 各セグメントに使えるのは ASCII の英数字・`.`・`_`・`-` です。
- 使えない名前: 空のセグメント、`.` / `..`、セグメント先頭の `-` や `.`、`.lock` で
  終わるセグメント、空白・制御文字を含む名前。

違反した場合、エラーメッセージにどの文字・どのセグメントが不正かが示されます。

### ベースブランチと fetch

作成元のベースブランチは **`--base` フラグ ＞ `[repos.<repo>]` の `base` ＞
`[defaults]` の `base` ＞ `auto`** の順に解決されます（[Configuration](configuration.md)
を参照）。fetch は `git fetch --no-tags <remote> <base>` で対象ブランチ 1 本だけを
取得します。fetch に失敗しても、既存の追跡 ref（`refs/remotes/<remote>/<base>`）が
あればそれを使って作成を続行するため、オフラインでも動作します。

認証はローカルの `git` コマンドに委譲されます（SSH agent・credential helper がそのまま
使われます）。対話的な認証プロンプトは無効化されているため、認証情報が無い場合は
ハングせず失敗します。

### スキップと失敗の条件

作成先ディレクトリ（`<worktrees_dir>/<name>/<repo>/`）の状態ごとに:

- **既存の worktree がある**: スキップします（冪等な再実行の基盤）。
- **空ディレクトリ**: 残骸として削除してから作成します。
- **無関係な中身がある**: 失敗します（上書きしません）。

同名のブランチが既に存在する場合も失敗します（履歴が異なる可能性があるため、黙って
再利用しません）。ブランチを削除するか別の名前を使ってください。

1 つのリポジトリの失敗は他のリポジトリの処理を止めません。結果はまとめて報告され、
1 件でも失敗があれば exit 1 です。

## `list` — worktree の一覧

```sh
wt list [--json]
```

`worktrees_dir` を実スキャンし、各 worktree の所属リポジトリ（ブランチ・健全性）・
別名・稼働中のサーバーを 1 つの表に統合して表示します。

```
WORKTREE   ALIAS               REPOS       SERVERS
ABC-123    ログイン画面の修正    api, web    api/backend: 稼働中 (pid 4242)
fix-crash  -                   api         -
(!) old-x  -                   web（壊れた gitdir） ← doctor --fix で修復
```

壊れたチェックアウト（元リポジトリの削除・作り直しなどで実体を失った gitdir ポインタ）
を含む worktree には `(!)` が付き、`doctor --fix` での修復が案内されます。

## `enter` — 既存 worktree への遷移

```sh
wt enter <name>
```

`<name>` の worktree ルートが存在することを確認し、**`after` フックだけ**を実行します。
リポジトリの探索・作成・`before` フックは行いません。cmux などへの自動遷移フック
（[Hooks](hooks.md) を参照）を「ただそこへ移動するだけ」の用途で使うための
コマンドです。

作成（または未作成分の追加）もしたい場合は `wt create <name>` を使います。`after`
フックは `create` の完了後にも実行されるため、遷移フックの設定は両者で共通です。

## `remove` — worktree の削除

```sh
wt remove <name> [--force] [--keep-branch]
```

以下を順に行います:

1. その worktree で稼働中のサーバーをすべて停止します（失敗したら中断します —
   稼働中のプロセスを残したままチェックアウトやログを消さないため）。
2. 各メンバーリポジトリを `git worktree remove` で削除します。未コミットの変更が
   あると git が拒否します（`--force` で上書き）。続けてブランチも削除します
   （`--keep-branch` で残せます）。
3. サーバーの setup 記録からこの worktree を取り除きます（同名で作り直したときに
   setup が再実行されるようにするため）。
4. 別名を削除します。
5. この worktree のログ（`.prev` 世代を含む）を削除します。
6. worktree ルートディレクトリを削除します（メンバーの削除がすべて成功した場合のみ）。

途中の失敗は結果に集約され、進められるところまで進めたうえで最後にエラーを返します。

## `repos` — リポジトリの一覧

```sh
wt repos
```

`repos_dir` 直下（再帰なし）で Git リポジトリとして検出されたディレクトリを名前順に
表示します。`create --repo` に指定できる名前の一覧でもあります。`.git` を持つ
ディレクトリが対象です（`.git` はディレクトリ・ファイルのどちらでも構いません）。

## `doctor` — 自己診断・自己修復

```sh
wt doctor [--fix] [--json]
```

8 種類のチェックを行い、発見（Finding）を一覧します。`--fix` を付けると修復可能な
発見をその場で修復します（付けない場合は報告のみ）。発見があること自体はエラー扱い
ではありません（exit 0）。

| チェック | 内容 | `--fix` の動作 |
| --- | --- | --- |
| `dead_running` | 稼働記録があるが実プロセスが消滅している | 記録をクリア |
| `stale_setup` | setup 記録がある worktree が実在しない | 記録を削除 |
| `stale_alias` | 別名が設定された worktree が実在しない | 別名を削除 |
| `orphan_log` | どこからも参照されないログファイル | ファイルを削除 |
| `prunable_worktrees` | `git worktree prune` で掃除できる残骸がある | `git worktree prune` を実行 |
| `broken_repo` | `repos_dir` のエントリの `.git` が無効 | 報告のみ |
| `config_without_repo` | 設定 `[repos.<name>]` があるが `repos_dir` に実在しない | 報告のみ |
| `repo_without_servers` | `repos_dir` に実在するがサーバー設定が無い | 報告のみ |

`rm -rf` で worktree を手動削除した場合の残骸は、このコマンドで一括して片付けられます
（`.git/worktrees/` に残るメタデータは、同名で作り直す際にも自動で prune されます）。

## `server` — dev サーバーの管理

`server switch` / `status` / `stop` / `logs` の詳細は
[Server Management](server-management.md) を参照してください。

## `alias` — worktree の表示名

`alias set` / `list` / `remove` の詳細は
[Server Management](server-management.md#alias) を参照してください。

## `config check` — 設定ファイルの検証

```sh
wt config check
```

設定ファイルを既定パスから読み込み、スキーマと必須フィールドを検証します。ファイルが
無い場合は「既定値で動作します」と表示して exit 0、存在して正常なら exit 0、不正なら
検証エラーを標準エラーへ出力して exit 1 です。CI で設定ファイルの妥当性だけを検査
したい場合に使えます。

## `mcp` — MCP サーバーとして起動

```sh
wt mcp
```

詳細は [MCP Server](mcp-server.md) を参照してください。

## フラグ / 環境変数

| フラグ | 環境変数 | 既定値 | 対応コマンド |
| --- | --- | --- | --- |
| `--repos-dir` | `WT_REPOS_DIR` | `~/repositories` | `create` / `server *` |
| `--worktrees-dir` | `WT_WORKTREES_DIR` | `~/worktrees` | `create` / `server *` |
| `--remote` | `WT_REMOTE` | `origin` | `create` |
| `--concurrency`, `-j` | `WT_CONCURRENCY` | 自動 | `create` |
| `--base` | – | `auto` | `create` |

その他のコマンド（`list` / `enter` / `remove` / `doctor` / `repos` / `alias`）は、
設定ファイルと環境変数からディレクトリを解決します。

優先順位はすべての項目で共通です:
**コマンドラインフラグ ＞ 環境変数 ＞ 設定ファイル ＞ 組み込みの既定値**
（[Configuration](configuration.md) を参照）。

## 終了コード

| コード | 意味 |
| --- | --- |
| `0` | 成功 |
| `1` | エラー |
| `130` | キャンセル（Ctrl-C / SIGTERM、対話プロンプトの中断を含む） |

Ctrl-C のキャンセルは、実行中の git・フック・サーバーのライフサイクルコマンドまで
伝播します（detach 起動済みのサーバー本体は対象外です）。
