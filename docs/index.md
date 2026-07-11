# worktree-integrator

複数の Git リポジトリの worktree をまとめて作成・管理する CLI ツールです。

- **worktree の一括作成**: 対象ディレクトリ（既定 `~/repositories/`）配下の複数
  リポジトリに、同じ名前の worktree をまとめて並列作成
- **一覧・遷移・削除・自己診断**: `list` / `enter` / `remove` / `doctor` で
  ライフサイクルを管理
- **dev サーバーの管理**: リポジトリごとの複数サーバーを、worktree の切り替えに
  合わせて起動・停止・ログ確認（`server` サブコマンド）
- **フック / 追加ファイルコピー**: 作成前後の任意コマンド実行や、`.env` など
  未追跡ファイルの引き継ぎ
- **MCP サーバーとしても利用可能**: LLM エージェントや IDE から同じ機能をツール
  として呼び出せます

## クイックスタート

!!! tip "推奨: Claude Code プラグインでセットアップ"
    Claude Code を使っているなら、バイナリの導入・MCP 登録・設定ファイルの作成・
    検証までを対話的に案内する[公式プラグイン](claude-code-plugin.md)での
    セットアップが推奨です。

```sh
go install github.com/vimrak-hal/worktree-integrator/cmd/worktree-integrator@latest
```

以降のページでは、読みやすさのため `worktree-integrator` に `wt` という短い名前を
付けたものとして表記します（[Installation](installation.md) を参照）。

```sh
wt my-feature                         # ~/repositories 直下から対話的に選択して作成
wt my-feature --all                   # 全リポジトリに作成（非対話）
wt my-feature --repo api --repo web   # 指定リポジトリだけに作成（非対話・繰り返し可）

wt my-feature                         # 再実行しても壊れない: 未作成のリポジトリだけ差分で追加
wt list                               # 作成済みの worktree 一覧（別名・稼働サーバーつき）
wt remove my-feature                  # サーバー停止 → worktree/ブランチ削除 → 後片付け
```

## よくある目的

| やりたいこと | 見るところ |
| --- | --- |
| 初期セットアップを対話的に済ませる | [Claude Code Plugin](claude-code-plugin.md)（推奨） |
| worktree をまとめて作る・消す | [Usage](usage.md) |
| 作成した worktree へ自動で移動する | [Hooks](hooks.md) の「worktree への自動遷移」 |
| `.env` などを新しい worktree へ引き継ぐ | [Copy Files](copy-files.md) |
| worktree ごとに dev サーバーを切り替える | [Server Management](server-management.md) |
| サーバーログを対話的に見る・切り替える | [Terminal UI](tui.md) |
| worktree に分かりやすい表示名を付ける | [Server Management](server-management.md#alias) |
| LLM エージェントから操作する | [MCP Server](mcp-server.md) |
| 手動削除の残骸や状態のずれを直す | [Usage](usage.md) の `doctor` |

## はじめに

<div class="grid cards" markdown>

-   :material-robot: **Claude Code Plugin（推奨）**

    ---

    セットアップを対話的に進めるプラグイン（スキル + 診断エージェント）

    [:octicons-arrow-right-24: 詳しく見る](claude-code-plugin.md)

-   :material-download: **Installation**

    ---

    インストール / ビルド

    [:octicons-arrow-right-24: 詳しく見る](installation.md)

-   :material-console: **Usage**

    ---

    コマンドリファレンス・フラグ / 環境変数・終了コード

    [:octicons-arrow-right-24: 詳しく見る](usage.md)

</div>

## 機能

<div class="grid cards" markdown>

-   :material-server: **Server Management**

    ---

    dev サーバーの切り替え・別名（`server` / `alias` サブコマンド）

    [:octicons-arrow-right-24: 詳しく見る](server-management.md)

-   :material-monitor: **Terminal UI**

    ---

    ログ閲覧と worktree 切り替えの対話 UI（`ui` サブコマンド。引数なしの `wt` でも開く）

    [:octicons-arrow-right-24: 詳しく見る](tui.md)

-   :material-api: **MCP Server**

    ---

    MCP サーバーとして使う（`mcp` サブコマンド）

    [:octicons-arrow-right-24: 詳しく見る](mcp-server.md)

</div>

## 設定

<div class="grid cards" markdown>

-   :material-cog: **Configuration**

    ---

    設定ファイル（`~/.config/worktree-integrator/config.toml`）の全体像

    [:octicons-arrow-right-24: 詳しく見る](configuration.md)

-   :material-webhook: **Hooks**

    ---

    フック（before / after_worktree / after）

    [:octicons-arrow-right-24: 詳しく見る](hooks.md)

-   :material-content-copy: **Copy Files**

    ---

    worktree への追加ファイルのコピー

    [:octicons-arrow-right-24: 詳しく見る](copy-files.md)

</div>

## プロジェクト

<div class="grid cards" markdown>

-   :material-sitemap: **Architecture**

    ---

    ディレクトリ構成・設計・実装ノート

    [:octicons-arrow-right-24: 詳しく見る](architecture.md)

-   :material-test-tube: **Testing**

    ---

    テスト

    [:octicons-arrow-right-24: 詳しく見る](testing.md)

</div>
