# worktree-integrator

複数の Git リポジトリの worktree をまとめて作成・管理する CLI ツールです。

`<name>` を指定すると、対象ディレクトリ（既定 `~/repositories/`）配下の Git リポジトリから
worktree を作る相手を選び、各リポジトリのリモート既定ブランチ（main / master 等を自動検出）を
fetch して `~/worktrees/<name>/<リポジトリ名>/` に linked worktree を**並列に**作成します。

作成は **冪等** です。同じ名前で再度実行しても既存のチェックアウトは壊さず、まだ作成していない
リポジトリだけを差分で追加します（対話モードでは未作成のリポジトリだけが選択肢になり、
全リポジトリが作成済みなら「追加するリポジトリはありません」で正常終了します）。作成した
worktree は `list` で一覧、`enter` で（サーバー起動などの `after` フックだけを実行して）遷移、
`remove` で完全な後始末付きで削除、`doctor` で状態のずれを自己診断・自己修復できます。加えて、
リポジトリごとの dev サーバーの起動・切替（`server switch/status/stop/logs`）や、MCP
（Model Context Protocol）サーバーとしての利用にも対応しています。

## クイックスタート

```sh
go install github.com/vimrak-hal/worktree-integrator/cmd/worktree-integrator@latest
```

以降のページでは、読みやすさのため `worktree-integrator` に `wt` という短い名前を付けた
（PATH 上にシンボリックリンクを張るか、シェルエイリアスを設定した）ものとして表記します。

```sh
wt my-feature                         # ~/repositories 直下から対話的に選択して作成
wt my-feature --all                   # 全リポジトリに作成（非対話）
wt my-feature --repo api --repo web   # 指定リポジトリだけに作成（非対話・繰り返し可）

wt my-feature                         # 再実行しても壊れない: 未作成のリポジトリだけ差分で追加
wt list                                # 作成済みの worktree 一覧（別名・稼働サーバーつき）
wt remove my-feature                  # サーバー停止 → worktree/ブランチ削除 → 状態・別名・ログの後片付け
```

## ページ一覧

| ページ | 内容 |
| --- | --- |
| [Installation](installation.md) | インストール / ビルド |
| [Usage](usage.md) | コマンド一覧・フラグ / 環境変数 |
| [Configuration](configuration.md) | 設定ファイル（`~/.config/worktree-integrator/config.toml`） |
| [Hooks](hooks.md) | フック（before / after_worktree / after） |
| [Copy Files](copy-files.md) | worktree への追加ファイルのコピー（`[defaults.copy]` / `[repos.<repo>.copy]`） |
| [Server Management](server-management.md) | サーバー管理（`server` サブコマンド） |
| [Alias](alias.md) | worktree の別名（`alias` サブコマンド） |
| [MCP Server](mcp-server.md) | MCP サーバーとして使う（`mcp` サブコマンド） |
| [Behavior](behavior.md) | 動作仕様（差分作成・`enter` への移行案内など） |
| [Architecture](architecture.md) | ディレクトリ構成・設計 |
| [Testing](testing.md) | テスト |
