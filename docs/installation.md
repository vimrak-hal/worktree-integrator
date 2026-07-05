# インストール / ビルド

!!! tip "推奨: Claude Code プラグインでセットアップ"
    Claude Code を使っているなら、バイナリの導入から設定ファイルの作成・検証までを
    対話的に案内する[公式プラグイン](claude-code-plugin.md)でのセットアップが
    推奨です。このページは手動でインストールする場合の手順です。

[Go](https://go.dev/)（1.25 以降）で実装されています。

```sh
go build -o worktree-integrator ./cmd/worktree-integrator
# あるいはインストール:
go install github.com/vimrak-hal/worktree-integrator/cmd/worktree-integrator@latest
```

ビルドに C コンパイラや CMake は不要です。Git 操作はローカルの `git` コマンドで行う
ため、実行時の要件は `git` が PATH 上にあることだけです。

生成されるバイナリ名は `worktree-integrator` です。他のページの例では、読みやすさの
ためこれを `wt` にリネームまたはシンボリックリンクしたものとして記載しています
（必須ではありません）:

```sh
ln -s "$(command -v worktree-integrator)" ~/.local/bin/wt
```

バージョンは `worktree-integrator --version`（`-v`）で確認できます。

## MCP サーバーとして使う場合

ビルドしたバイナリをそのまま MCP サーバーとして登録できます（追加のインストール
手順は不要です）。詳細は [MCP Server](mcp-server.md) を参照してください。

## 次に読むページ

- [Usage](usage.md): コマンドリファレンス・フラグ / 環境変数
- [Configuration](configuration.md): 設定ファイルの作成（`repos_dir` /
  `worktrees_dir` の変更、hooks・server・copy の定義）
