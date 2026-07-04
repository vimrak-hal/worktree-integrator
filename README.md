# worktree-integrator

[![CI](https://github.com/vimrak-hal/worktree-integrator/actions/workflows/ci.yml/badge.svg)](https://github.com/vimrak-hal/worktree-integrator/actions/workflows/ci.yml)

複数の Git リポジトリの worktree をまとめて作成・管理する CLI ツールです。

`<name>` を指定すると、対象ディレクトリ（既定 `~/repositories/`）配下の Git リポジトリから
worktree を作る相手を選び、各リポジトリのリモート既定ブランチ（main / master 等を自動検出）を
fetch して `~/worktrees/<name>/<リポジトリ名>/` に linked worktree を**並列に**作成します。
作成は**冪等**です。同じ名前で再度実行しても既存のチェックアウトは壊さず、まだ作成していない
リポジトリだけを差分で追加します。

作成した worktree は一覧・削除・自己診断でき、リポジトリごとの dev サーバーの起動・切替、
MCP（Model Context Protocol）サーバーとしての利用にも対応しています。

## クイックスタート

[Go](https://go.dev/)（1.25 以降）が必要です。

```sh
go install github.com/vimrak-hal/worktree-integrator/cmd/worktree-integrator@latest
```

以降の例では、読みやすさのため `worktree-integrator` に `wt` という短い名前を付けた
（PATH 上にシンボリックリンクを張るか、シェルエイリアスを設定した）ものとして表記します。

```sh
wt my-feature                         # ~/repositories 直下から対話的に選択して作成
wt my-feature --all                   # 全リポジトリに作成（非対話）
wt my-feature --repo api --repo web   # 指定リポジトリだけに作成（非対話・繰り返し可）

wt my-feature                         # 再実行しても壊れない: 未作成のリポジトリだけ差分で追加
wt list                                # 作成済みの worktree 一覧（別名・稼働サーバーつき）
wt remove my-feature                  # サーバー停止 → worktree/ブランチ削除 → 状態・別名・ログの後片付け
```

Git 操作は **ローカルの `git` コマンド**で行うため、**実行時に `git` が PATH 上にある**ことだけが要件です。

## ドキュメント

詳しい使い方は [ドキュメントサイト](https://vimrak-hal.github.io/worktree-integrator/) を参照してください。
導入直後は [Home](https://vimrak-hal.github.io/worktree-integrator/) →
[Installation](https://vimrak-hal.github.io/worktree-integrator/installation/) →
[Usage](https://vimrak-hal.github.io/worktree-integrator/usage/) →
[Behavior](https://vimrak-hal.github.io/worktree-integrator/behavior/) の順に読むのがおすすめです。

| ページ | 内容 |
| --- | --- |
| [Home](https://vimrak-hal.github.io/worktree-integrator/) | 概要とページ一覧 |
| [Installation](https://vimrak-hal.github.io/worktree-integrator/installation/) | インストール / ビルド |
| [Usage](https://vimrak-hal.github.io/worktree-integrator/usage/) | コマンド一覧・フラグ / 環境変数 |
| [Configuration](https://vimrak-hal.github.io/worktree-integrator/configuration/) | 設定ファイル（`~/.config/worktree-integrator/config.toml`） |
| [Hooks](https://vimrak-hal.github.io/worktree-integrator/hooks/) | フック（before / after_worktree / after） |
| [Copy Files](https://vimrak-hal.github.io/worktree-integrator/copy-files/) | worktree への追加ファイルのコピー |
| [Server Management](https://vimrak-hal.github.io/worktree-integrator/server-management/) | サーバー管理（`server` サブコマンド） |
| [Alias](https://vimrak-hal.github.io/worktree-integrator/alias/) | worktree の別名（`alias` サブコマンド） |
| [MCP Server](https://vimrak-hal.github.io/worktree-integrator/mcp-server/) | MCP サーバーとして使う（`mcp` サブコマンド） |
| [Behavior](https://vimrak-hal.github.io/worktree-integrator/behavior/) | 動作仕様（差分作成・`enter` への移行案内など） |
| [Architecture](https://vimrak-hal.github.io/worktree-integrator/architecture/) | ディレクトリ構成・設計 |
| [Testing](https://vimrak-hal.github.io/worktree-integrator/testing/) | テスト |

## テスト

```sh
go build ./...
go test -race ./...
go vet ./...
gofmt -l .             # 未整形ファイルの一覧（空なら整形済み）
```

詳細（クロスコンパイルの検証など）は [Testing](https://vimrak-hal.github.io/worktree-integrator/testing/) を参照してください。
