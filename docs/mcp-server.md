# MCP サーバーとして使う（`mcp` サブコマンド）

CLI と同じワークフロー（worktree の作成・一覧・削除、サーバー管理、別名）を
**MCP（Model Context Protocol）サーバー**として公開できます。LLM エージェントや
IDE などの MCP クライアントからツールとして呼び出せます。

```sh
worktree-integrator mcp
```

stdio 上で JSON-RPC を話す MCP サーバーとして起動し、クライアントが切断するまで
動作します。各ツールは CLI と同じオーケストレーションを呼び出す薄いアダプタです。

## 公開ツール

| ツール | 対応する CLI | 概要 |
| --- | --- | --- |
| `repos_list` | `wt repos` | `repos_dir` 直下の Git リポジトリ一覧 |
| `worktree_create` | `wt create` | `repos` で指定した各リポジトリに worktree を作成 |
| `worktree_list` | `wt list` | worktree の一覧（別名・メンバー・稼働サーバー） |
| `worktree_remove` | `wt remove` | worktree の削除（サーバー停止・後片付けを含む） |
| `server_switch` | `wt server switch` | サーバーを指定 worktree へ切り替え |
| `server_status` | `wt server status` | リポジトリ・サーバーごとの状態 |
| `server_stop` | `wt server stop` | 稼働中サーバーの停止 |
| `server_logs` | `wt server logs` | サーバーログの末尾を表示 |
| `alias_set` / `alias_list` / `alias_remove` | `wt alias …` | 別名の設定・一覧・削除 |

## CLI との違い

- **対話選択はありません。** `worktree_create` は対象リポジトリを `repos` で明示
  指定します（候補は `repos_list` で取得します）。
- **`worktree_remove` に `--force` 相当はありません。** 未コミットの変更がある
  チェックアウトの削除はエラーとして返ります。強制削除は CLI から行います。
- **`server_logs` は追従（`tail -f`）しません。** 末尾の `lines` 行（既定 50・
  最大 2000）だけを返します。CLI の `-f` や [ターミナル UI](tui.md)（`wt ui`）は
  CLI 専用の表示手段であり、MCP のツール語彙には存在しません（stdio を JSON-RPC が
  占有するため）。逆に、`wt ui` を開いたままエージェントに `server_switch` させる
  使い方は想定内です — TUI のログ・状態表示は外部の切り替えへ自動で追従します。
- **ディレクトリのオーバーライドはありません。** `repos_dir` / `worktrees_dir` は
  設定ファイルと MCP サーバープロセスの環境変数から解決され、ツール呼び出しごとに
  変えることはできません。

設定ファイルはツール呼び出しのたびに読み直されるため、編集は MCP サーバーの再起動
なしで反映されます。フックやサーバーのライフサイクルコマンドの出力は標準エラーへ
流れ、MCP クライアントのログで確認できます（仕組みは [Architecture](architecture.md)
を参照）。

## MCP クライアントの設定例

stdio 起動の MCP サーバーを登録する一般的な設定例です（クライアントによってキー名は
異なります）。

```json
{
  "mcpServers": {
    "worktree-integrator": {
      "command": "/path/to/worktree-integrator",
      "args": ["mcp"]
    }
  }
}
```

Claude Code であれば次のように登録できます。

```sh
claude mcp add worktree-integrator -- /path/to/worktree-integrator mcp
```
