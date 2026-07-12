# worktree-integrator (Claude Code plugin)

[worktree-integrator](https://github.com/vimrak-hal/worktree-integrator) 本体の
初期セットアップとトラブルシューティングを支援する Claude Code 用プラグインです。
バイナリのビルド／インストール、MCP サーバー登録、設定ファイル（`repos_dir` /
base ブランチ / dev サーバー定義 / hooks / 追加ファイルコピー）の作成から、
`config check` / `doctor` による検証までを Claude と対話しながら進められます。

## インストール

```
/plugin marketplace add vimrak-hal/worktree-integrator
/plugin install worktree-integrator@worktree-integrator
```

## アップデート

このプラグインを既に入れている場合、リポジトリ側の更新を取り込むには2段階の操作が
必要です（自動更新はされません）。

```
/plugin marketplace update worktree-integrator   # マーケットプレイス側のキャッシュを最新化
/plugin update worktree-integrator@worktree-integrator   # インストール済みプラグインを最新版に更新
```

反映には Claude Code の再起動が必要です。マーケットプレイス自体を最新化せずに
`plugin update` だけ実行しても、キャッシュされた古い内容のままになるので注意してください。

## 収録スキル

- `worktree-integrator-setup` — 初期セットアップ手順書。dev サーバー管理
  （`server switch/status/stop/logs`、MCP の `server_*` ツール）が no-op になる、
  初めて導入する、設定ファイルがパースエラーで無視される、といった場面で Claude が
  自動的に参照します。詳細は 2 つのリファレンスに分かれています:
  - `references/config-reference.md` — config.toml スキーマ v2 の全キー
    （base 解決順・copy のマージ規則・hooks/servers の環境変数まで）
  - `references/troubleshooting.md` — 症状→原因→対処の一覧と `doctor` の
    全チェック対応表

## 収録エージェント

- `worktree-integrator-doctor` — 環境不調の一次調査を行う**読み取り専用**の診断
  エージェント。バイナリ・設定ファイル・リポジトリ探索・`doctor` 所見・サーバー状態・
  MCP 登録を一括で調査し、原因と対処コマンドをレポートします（修復そのものは行わず、
  提案のみ）。「セットアップ済みなのに動かない」ときに Claude が自動で起動するか、
  明示的に依頼して使います。
- `worktree-integrator-hooks` — ライフサイクルフック（config.toml の `[[hooks.*]]`）の
  設計・記述・検証を専門に行うエージェント。「worktree 作成後に依存をインストール
  したい」「作成完了を通知したい」「Jira / cmux と連携したい」といった依頼から、
  タイミング（`before` / `after_worktree` / `after`）と `WT_*` 環境変数を選び、既存の
  設定を壊さずマージして `config check` で検証まで行います。hooks 以外の設定
  （servers / copy / base 等）には触れません。

詳しいツール本体の使い方は [ドキュメントサイト](https://vimrak-hal.github.io/worktree-integrator/)
を参照してください。
