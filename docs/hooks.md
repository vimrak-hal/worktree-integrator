# フック（hooks）

ワークフローの各タイミングで任意のシェルコマンド（**フック**）を実行できます。
フックは設定ファイルの `[[hooks.<タイミング>]]` テーブルに記述し、**同じタイミングの
フックはすべて並列に実行**されます。Jira チケットの確認のような前処理や、worktree
作成後のサーバー起動などに利用できます。

## タイミング

| タイミング | 実行回数 | 作業ディレクトリ | 用途の例 |
| --- | --- | --- | --- |
| `before` | 全体で 1 回（リポジトリ探索の前） | 呼び出し時のディレクトリ | Jira チケットの確認・事前検証 |
| `after_worktree` | 作成された worktree ごとに 1 回 | その worktree | 依存関係のインストール、サーバー起動 |
| `after` | 全体で 1 回（全処理の後） | 呼び出し時のディレクトリ | 通知・集約処理 |

## フックの項目

| キー | 既定値 | 説明 |
| --- | --- | --- |
| `name` | （必須） | 進捗・サマリ表示に使う名前 |
| `command` | （必須） | `sh -c` で解釈されるコマンド。**文字列**（パイプや `&&` も使用可）か、**文字列の配列**で複数コマンドを指定できる |
| `background` | `false` | `true` のとき、完了を待たずに起動だけする（サーバー等の常駐プロセス向け） |
| `allow_failure` | `false` | `true` のとき、失敗しても全体を失敗扱いにせず警告にとどめる |
| `workdir` | （任意） | 作業ディレクトリの明示指定。未指定時は上表の既定に従う |

`command` に配列を渡すと、各コマンドを **記述順に逐次実行** します（内部的に `&&` で連結）。
途中で失敗するとそこで打ち切られ、フック全体が失敗扱いになります（`allow_failure` 指定時は
警告）。`background = true` のフックでは、配列全体が 1 つのプロセスとしてまとめて起動されます。

## 環境変数

各フックには以下の環境変数が渡されます。

| 変数 | 内容 |
| --- | --- |
| `WT_WORKTREE_NAME` | worktree 名（＝ブランチ名） |
| `WT_REPOS_DIR` | 対象リポジトリのベースディレクトリ |
| `WT_WORKTREES_DIR` | worktree 作成先のベースディレクトリ |
| `WT_ROOT` | 今回の worktree ルート（`<worktrees_dir>/<worktree名>`） |
| `WT_REPO_NAME` | リポジトリ名（`after_worktree` のみ） |
| `WT_REPO_PATH` | 元リポジトリのパス（`after_worktree` のみ） |
| `WT_WORKTREE_PATH` | そのリポジトリの worktree パス（`after_worktree` のみ） |

## 設定例

```toml
# ~/.config/worktree-integrator.toml

# 前処理: Jira チケットを確認（バックグラウンドで後続処理と並行実行）
[[hooks.before]]
name       = "jira-check"
command    = "jira issue view $WT_WORKTREE_NAME"
background = true

# worktree 作成後: 依存関係をインストールしてビルド（配列で複数コマンドを逐次実行）
[[hooks.after_worktree]]
name    = "setup"
command = ["npm ci", "npm run build"]

# worktree 作成後: 開発サーバーを起動（常駐するのでバックグラウンド）
[[hooks.after_worktree]]
name       = "dev-server"
command    = "npm run dev > dev-server.log 2>&1"
background = true

# 後処理: 通知（失敗しても全体は失敗扱いにしない）
[[hooks.after]]
name          = "notify"
command       = "notify-send \"worktree $WT_WORKTREE_NAME 作成完了\""
allow_failure = true
```

> `before` フックが失敗（`allow_failure` 未指定）した場合は、リポジトリ処理に入る前に
> 中断します。`after_worktree` / `after` フックの失敗は、当該処理は続行しつつ、最終的な
> 終了コードを非ゼロにします。

## サンプルスクリプト

すぐ使えるフックのサンプルを
[`examples/hooks/`](https://github.com/vimrak-hal/worktree-integrator/tree/main/examples/hooks)
に置いています。

| スクリプト | タイミング | 概要 |
| --- | --- | --- |
| [`cmux-jira-title.sh`](https://github.com/vimrak-hal/worktree-integrator/blob/main/examples/hooks/cmux-jira-title.sh) | `before` | worktree 名が Jira チケット形式（例: `ABC-123`）のとき、Jira MCP からタイトルを取得して cmux のタブタイトルに設定する |
| [`cmux-open-worktree.sh`](https://github.com/vimrak-hal/worktree-integrator/blob/main/examples/hooks/cmux-open-worktree.sh) | `after` | 作成（または既存への再呼び出し）後に、いま操作しているターミナル（cmux サーフェス）自体を worktree ルート（`WT_ROOT`）へ `cd` させる。新しいタブ／ワークスペースは開かない。MCP 実行時は対象ターミナルが無いので何もしない |

```toml
# ~/.config/worktree-integrator.toml
[[hooks.before]]
name          = "cmux-jira-title"
command       = "/path/to/worktree-integrator/examples/hooks/cmux-jira-title.sh"
allow_failure = true   # Jira 取得に失敗しても worktree 作成は止めない
```

スクリプト先頭のコメントに、必要な依存（mcptools の `mcp` コマンド・`jq`・Jira MCP
サーバー）と、環境変数（`JIRA_MCP_CMD` / `JIRA_MCP_TOOL` など）による差し替え方法を
記載しています。社内 CLI や REST API を使う場合は `fetch_jira_title()` を置き換えて
ください。

### worktree への自動遷移（cmux）

CLI バイナリは子プロセスなので、呼び出し元シェルの作業ディレクトリ（`cd`）を直接
変えることはできません。代わりに cmux を使っている場合は、`after` フックから cmux を
ソケット経由で操作して、**いま操作しているターミナル（cmux サーフェス）自体を**
作成した worktree へ `cd` させられます（新しいタブ／ワークスペースは開きません）。

```toml
# ~/.config/worktree-integrator.toml
[[hooks.after]]
name          = "cmux-open-worktree"
command       = "/path/to/worktree-integrator/examples/hooks/cmux-open-worktree.sh"
allow_failure = true   # cmux 操作に失敗しても worktree 作成は止めない
```

- 仕組み: `cmux send` で現在のサーフェスに `cd <worktree ルート>` ＋ Enter を送り込みます。
  コマンドが終了してプロンプトに戻った瞬間に、そのシェルが当該 worktree へ移動します
  （タイプアヘッド）。worktree ルートは `<worktrees_dir>/<worktree名>` です。
- **MCP 実行時の安全性**: MCP サーバー経由では「呼び出し元の対話ターミナル」が存在せず、
  無条件に `cd` を送ると無関係なターミナル（例: エージェントのペイン）へ文字列が
  入力されてしまいます。フックは stdin が本物のターミナル（tty）であることを対話 CLI の
  目印にし、その場合だけ `cd` を送ります。MCP 実行時（MCP サーバーが子プロセスの stdin を
  `/dev/null` に付け替える）は何もせず正常終了するため、誤入力は起きません。
- このフックは `WT_ROOT` を読むだけなので、cmux 以外（tmux / 任意の方法）で遷移したい
  場合は同様の `after` フックに差し替えてください。`cmux` が見つからない・対話ターミナル
  でない・cmux 操作に失敗した場合でも、フックは worktree 作成を止めずに正常終了します。

> **既存 worktree への再呼び出しは「遷移するだけ」**: worktree ルートが既に存在する場合、
> `worktree-integrator <名前>`（= `create <名前>`）はリポジトリ選択（チェックボックス）と
> 作成を丸ごとスキップし、`after` フックだけを実行します。これにより、上記の遷移フックと
> 組み合わせると「既にあるなら、ただそこへ移動する」挙動になります。新たにリポジトリを
> 追加したい場合は、対象の worktree ルートを削除してから作り直してください。
