# MCP サーバーとして使う（`mcp` サブコマンド）

CLI と同じワークフロー（worktree 作成・サーバー管理・別名）を **MCP（Model Context
Protocol）サーバー**として公開できます。LLM エージェントや IDE などの MCP クライアントから
ツールとして呼び出せます。

```sh
worktree-integrator mcp
```

stdio 上で JSON-RPC を話す MCP サーバーとして起動し、クライアントが切断するまで動作します。
内部実装は CLI の再実装ではなく、各ツールが CLI と同じ解決済みコマンドを組み立てて
**同じ `app` のオーケストレーションを呼び出す**薄いアダプタです。

## 公開ツール

| ツール | 対応する CLI | 概要 |
| --- | --- | --- |
| `list_repos` | （`create` の探索） | リポジトリディレクトリ直下の Git リポジトリ一覧 |
| `create_worktrees` | `create <名前>` | `repos` で**明示指定**した各リポジトリに worktree を作成 |
| `server_switch` | `server switch` | 対象リポジトリのサーバーを指定 worktree へ切替 |
| `server_status` | `server status` | リポジトリ・サーバーごとの状態（active worktree / 別名 / 稼働状態 / PID） |
| `server_stop` | `server stop` | 稼働中サーバーの停止 |
| `server_logs` | `server logs` | サーバーログの末尾を表示（`lines` 指定可、追従はしない） |
| `alias_set` / `alias_list` / `alias_get` / `alias_remove` | `alias …` | worktree 表示別名の設定・一覧・取得・削除 |

各ツールの `repos_dir` / `worktrees_dir` / `remote` などのパラメータは任意で、省略時は CLI と
同じ優先順位で解決されます。ディレクトリ系（`repos_dir` / `worktrees_dir`）は
**パラメータ ＞ `WT_*` 環境変数 ＞ 設定ファイル ＞ 既定値**、`remote` は
**パラメータ ＞ 設定ファイル ＞ `origin`**（こちらに環境変数はありません）です。サーバー定義や
フックは設定ファイルから読み込みます（ツール呼び出しごとに解決）。

## プロトコルの制約による CLI との違い

- **対話選択はできません。** ターミナルのチェックボックスは MCP 上で使えないため、
  `create_worktrees` は対象リポジトリを `repos` で**明示指定**します（候補は `list_repos`
  で取得してください）。
- **ログ追従はしません。** `server_logs` は末尾の一定行数だけを返します（`tail -f` は
  応答が返らないため）。

## stdio の取り扱い

JSON-RPC はこのプロセスの標準入出力を流れるため、それ以外の出力が混ざるとプロトコルが
壊れます。オーケストレーション自体の出力は内部バッファに捕捉して結果として返します。
フックやサーバーのライフサイクルコマンドが**起動する子プロセス**については、子プロセスの
標準ストリームを**明示的に指定**します（`internal/infra/childio`）。MCP モードでは子の `stdin` を
`/dev/null`、`stdout`/`stderr` を**標準エラー**へ向けるため、どの子プロセスもプロトコル
ストリーム（`fd 1`）に触れません（その出力はクライアントのログに見える標準エラーへ流れ
ます）。サーバーの `start` は元から detach + ログファイルへリダイレクトされているため影響を
受けません。

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
