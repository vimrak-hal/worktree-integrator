# worktree-integrator

[![CI](https://github.com/vimrak-hal/worktree-integrator/actions/workflows/ci.yml/badge.svg)](https://github.com/vimrak-hal/worktree-integrator/actions/workflows/ci.yml)

複数の Git リポジトリの worktree をまとめて作成・管理する CLI ツールです。

- **worktree の一括作成**: 対象ディレクトリ配下の複数リポジトリに、同じ名前の worktree を
  まとめて並列作成
- **一覧・遷移・削除・自己診断**: `list` / `enter` / `remove` / `doctor` でライフサイクルを管理
- **dev サーバーの管理**: リポジトリごとの複数サーバーを、worktree の切り替えに合わせて
  起動・停止（`server` サブコマンド）
- **ターミナル UI**: サーバーログの閲覧（対象切り替え・追従・絞り込み）と worktree の
  切り替えを 1 画面で（`ui` サブコマンド）
- **MCP サーバーとしても利用可能**: LLM エージェントや IDE から同じ機能をツールとして
  呼び出せます

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

## Claude Code プラグイン

初期セットアップ（設定ファイル・dev サーバー定義・MCP 登録・検証）を案内するスキルと、
環境不調を一次調査する読み取り専用の診断エージェントを Claude Code プラグインとして
配布しています。

```
/plugin marketplace add vimrak-hal/worktree-integrator
/plugin install worktree-integrator@worktree-integrator
```

詳細は [`plugins/worktree-integrator`](plugins/worktree-integrator/README.md) を参照してください。

## ドキュメント

詳しい使い方・全ページの一覧は [ドキュメントサイト](https://vimrak-hal.github.io/worktree-integrator/)
を参照してください。導入直後は [Home](https://vimrak-hal.github.io/worktree-integrator/) →
[Installation](https://vimrak-hal.github.io/worktree-integrator/installation/) →
[Usage](https://vimrak-hal.github.io/worktree-integrator/usage/) →
[Configuration](https://vimrak-hal.github.io/worktree-integrator/configuration/) の順に読むのがおすすめです。

## テスト

```sh
go build ./...
go test -race ./...
go vet ./...
gofmt -l .             # 未整形ファイルの一覧（空なら整形済み）
```

詳細（クロスコンパイルの検証など）は [Testing](https://vimrak-hal.github.io/worktree-integrator/testing/) を参照してください。
