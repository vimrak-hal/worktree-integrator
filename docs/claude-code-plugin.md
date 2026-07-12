# Claude Code プラグイン（推奨セットアップ）

worktree-integrator の初期セットアップは、**Claude Code プラグインを使って対話的に
進めるのが推奨**です。

初期セットアップには、バイナリの導入・MCP 登録・設定ファイル
（`~/.config/worktree-integrator/config.toml`）の作成・検証という複数の手順があり、
特に設定ファイルは手書きだと詰まりどころが多くあります:

- 未知キーや綴り違いは**設定全体が読み込まれない**パースエラーになる
- `[repos.<name>]` の `<name>` は `repos_dir` 直下のディレクトリ名と厳密に一致する
  必要がある（不一致だと `server` 系が黙って no-op になる）
- dev サーバー定義・hooks・copy はプロジェクトごとに内容が異なり、テンプレートの
  丸写しでは動かない

プラグインのスキルはこれらを把握したうえで、リポジトリの場所や起動コマンドを確認し
ながら設定を組み立て、`config check` / `doctor` による検証まで通しで案内します。

## インストール

Claude Code で次の 2 コマンドを実行します:

```
/plugin marketplace add vimrak-hal/worktree-integrator
/plugin install worktree-integrator@worktree-integrator
```

## セットアップの開始

インストール後、Claude Code に自然言語で依頼するだけでスキルが起動します:

```
worktree-integrator をセットアップして
```

以下の流れで対話的に進みます:

1. **バイナリの用意** — `go install`（またはソースからビルド）と配置の確認
2. **MCP サーバー登録**（任意・推奨） — `claude mcp add` または `.mcp.json`
3. **設定ファイルの作成** — `repos_dir` / ベースブランチ / dev サーバー定義
   （`[repos.<name>.servers.<server>]`）/ hooks / 追加ファイルコピーを、実際の
   環境を確認しながら記述。既存の設定があれば上書きせずマージ
4. **検証** — `config check` → `repos` → `doctor` → `server status` →
   実際の `server switch` まで確認

初回導入だけでなく、「`server_*` ツールが何も起きない」「設定がパースエラーで
無視される」といった症状のときも、同じスキルが自動的に参照されます。

## 診断エージェント

プラグインには**読み取り専用**の診断エージェント `worktree-integrator-doctor` も
収録されています。セットアップ済みの環境が動かなくなったときに、バイナリ・
設定ファイル・リポジトリ探索・`doctor` 所見・サーバー状態・MCP 登録を一括で調査し、
原因と対処コマンドをレポートします（環境への変更は一切行いません）。

```
worktree-integrator の調子が悪いので調べて
```

のように依頼すると、Claude が必要に応じてこのエージェントを起動します。

## hooks 設定エージェント

ライフサイクルフック（[Hooks](hooks.md)、config.toml の `[[hooks.*]]`）の設計・記述・
検証を専門に行うエージェント `worktree-integrator-hooks` も収録されています。
タイミング（`before` / `after_worktree` / `after`）や `WT_*` 環境変数の選択、
失敗時の方針（`allow_failure` / `timeout_secs`）を踏まえて設定を組み立て、既存の
config.toml を壊さずにマージし、`config check` による検証まで行います
（hooks 以外の設定には触れません）。

```
worktree 作成後に npm ci を流すようにして
```

のように依頼すると、Claude が必要に応じてこのエージェントを起動します。

## プラグインのアップデート

リポジトリ側の更新を取り込むには 2 段階の操作が必要です（自動更新はされません）:

```
/plugin marketplace update worktree-integrator
/plugin update worktree-integrator@worktree-integrator
```

反映には Claude Code の再起動が必要です。

## 手動でセットアップする場合

Claude Code を使わない場合は、次のページを順に読んでください:

1. [Installation](installation.md) — バイナリのビルド / インストール
2. [Configuration](configuration.md) — 設定ファイルの作成
3. [Server Management](server-management.md) — dev サーバー定義
4. [MCP Server](mcp-server.md) — MCP サーバーとしての登録（任意）
