# インストール / ビルド

[Go](https://go.dev/)（1.25 以降）で実装されています。

```sh
go build -o worktree-integrator ./cmd/worktree-integrator
# あるいはインストール:
go install github.com/vimrak-hal/worktree-integrator/cmd/worktree-integrator@latest
```

ビルドに C コンパイラや CMake は不要です。Git 操作は **ローカルの `git` コマンド**
（`os/exec`）で行うため、**実行時に `git` が PATH 上にある**ことだけが要件です
（libgit2 へのリンクはありません）。

`go build` / `go install` が生成するバイナリ名は `worktree-integrator` です（`cmd/`
配下のパッケージ名がそのままバイナリ名になります）。他ページの例では、読みやすさのため
これを `wt` にリネームまたはシンボリックリンクしたものとして記載しています。必須では
ないので、フルネームのまま使っても構いません。

```sh
ln -s "$(command -v worktree-integrator)" ~/.local/bin/wt
```

バージョン文字列は `worktree-integrator --version`（`-v`）で確認できます。`go install
module@version` でビルドした場合はそのモジュールバージョンが、リポジトリ内で素の
`go build` を行った場合はビルド情報から取得できる値（無ければ組み込みのフォールバック値）
が表示されます。

## MCP サーバーとして使う場合

ビルドしたバイナリをそのまま MCP サーバーとして登録できます（追加のインストール手順は
不要です）。詳細は [MCP Server](mcp-server.md) を参照してください。

## 次に読むページ

- [Usage](usage.md): コマンド一覧とフラグ / 環境変数
- [Configuration](configuration.md): 設定ファイルの作成（`repos_dir` / `worktrees_dir` の変更、
  hooks・server・copy の定義）
