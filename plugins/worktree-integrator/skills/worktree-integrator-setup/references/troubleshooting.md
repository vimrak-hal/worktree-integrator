# トラブルシューティング

「セットアップしたはずなのに動かない」ときの切り分け手順と、症状別の対処一覧。
環境全体の一次調査は読み取り専用の **worktree-integrator-doctor** エージェント
（このプラグインに同梱）に任せてもよい。

## 切り分けの順番

上から順に実行する。各ステップで問題が見つかったら、そこで直してから次へ進む。

```sh
worktree-integrator --version          # 1. バイナリは想定のものか
worktree-integrator config check       # 2. 設定は構文的に正しいか（exit 0/1）
worktree-integrator repos              # 3. リポジトリが見えているか（repos_dir の確認）
worktree-integrator doctor             # 4. 自己診断（下の対応表）
worktree-integrator server status      # 5. サーバー定義が見えているか・稼働状態
claude mcp get worktree-integrator     # 6. MCP 登録の確認（使っている場合）
```

## `doctor` のチェック対応表

`worktree-integrator doctor [--fix] [--json]` は 8 種類のチェックを行う。発見が
あること自体はエラーではない（exit 0）。`--fix` で「修復」列の操作をその場で行う。

| チェック | 内容 | `--fix` の動作 |
| --- | --- | --- |
| `dead_running` | 稼働記録があるが実プロセスが消滅している | 記録をクリア |
| `stale_setup` | setup 記録がある worktree が実在しない | 記録を削除 |
| `stale_alias` | 別名が設定された worktree が実在しない | 別名を削除 |
| `orphan_log` | どこからも参照されないログファイル | ファイルを削除 |
| `prunable_worktrees` | `git worktree prune` で掃除できる残骸 | prune を実行 |
| `broken_repo` | `repos_dir` のエントリの `.git` が無効 | 報告のみ（clone し直す等はユーザー判断） |
| `config_without_repo` | 設定 `[repos.<name>]` があるが実在しない | 報告のみ（`<name>` のタイプミス・移動済みの検出） |
| `repo_without_servers` | 実在するがサーバー設定が無い | 報告のみ（`server switch` の対象にならないことの可視化） |

`rm -rf` で worktree を手動削除した場合の残骸（稼働記録・setup 記録・別名・ログ・
git メタデータ）は `doctor --fix` で一括して片付く。

## 症状別の対処

### `server_*` が何も起きない /「サーバー設定がありません」

- `[repos.<name>.servers.<server>]` が未定義 → SKILL.md ステップ 3c で定義する。
- 定義したのに出ない → `<name>` が `repos_dir` **直下のディレクトリ名**と一致して
  いない。`worktree-integrator repos` の出力と設定のキーを突き合わせる
  （`doctor` の `config_without_repo` でも検出できる）。
- 設定ファイル自体が読めていない可能性もある → 次項。

### 設定がまるごと無視される / 直したはずが反映されない

- 未知キー・綴り違いは TOML パースエラーになり、既定値へのフォールバックではなく
  **設定全体が読み込まれない**。`worktree-integrator config check` で確認する
  （エラーメッセージにどのキーが不正か表示される）。
- 旧スキーマ（v1）のキーには移行ヒント付きのエラーが出る:
  - `[servers.<repo>.<server>]` → `[repos.<repo>.servers.<server>]` へ移動
  - トップレベル `[copy]` → 共通は `[defaults.copy]`、リポジトリ別は `[repos.<repo>.copy]` へ移動
  - hooks の `background` → 廃止。常駐プロセスは servers 定義で管理
- 編集先のパスが違う可能性: 正しいのは `~/.config/worktree-integrator/config.toml`
  （ディレクトリの中）。`~/.config/worktree-integrator.toml`（拡張子直付け）は
  **読み込まれない死んだパス**なので、あれば内容を新パスへ移行して消す。
  `XDG_CONFIG_HOME` を設定している環境ではそちらが優先される点にも注意。
- MCP 経由の場合、設定ファイルは**ツール呼び出しのたびに読み直される**ので、MCP
  サーバーの再起動は不要。反映されないなら上記のパス・パースエラーを疑う。

### MCP のツールが見えない / 旧名称が見える / Not connected

- `claude mcp get worktree-integrator` で状態と登録コマンドのパスを確認する。
  パスが存在しないバイナリを指しているなら登録し直す。
- セッションに `list_repos` / `create_worktrees` / `alias_get` という**旧名称**が
  見える場合、接続先のバイナリが古い（現行は `repos_list` / `worktree_create` /
  `worktree_list` / `worktree_remove` などの noun_verb 統一名。`alias_get` は
  `alias_list` に統合され廃止）。バイナリを更新して再接続する。
- ツール一覧は**セッション開始時にしか読み込まれない**。バイナリを再ビルド／再配置
  したら Claude Code を再起動するか `/mcp` から再接続する。

### `create` が失敗する

- **ブランチが既に存在する**: 同名ブランチは黙って再利用しない（履歴が異なる可能性が
  あるため）。ブランチを削除するか別の worktree 名を使う。
- **worktree 名が不正**: 名前はブランチ名を兼ねるため `git check-ref-format` 準拠。
  エラーメッセージにどの文字・セグメントが不正か表示される。
- **非対話環境（パイプ・CI）**: 対話選択ができないので `--repo` か `--all` を明示する。
  MCP の `worktree_create` も `repos` の明示が必須（候補は `repos_list`）。
- **作成先に無関係な中身がある**: 上書きしない仕様。ディレクトリを退避・削除してから
  再実行する（空ディレクトリなら自動で削除して作成する）。
- fetch 失敗はオフラインでも既存の追跡 ref があれば続行する。認証はローカル `git` に
  委譲され、対話プロンプトは無効なので認証情報が無いとハングせず失敗する。

### サーバーがすぐ落ちる / ログを見たい

- `worktree-integrator server logs <worktree名>`（停止後・クラッシュ後も閲覧可）。
  `-n N` で行数、`--prev` で 1 世代前（起動時ローテーション分）、CLI のみ `-f` で追従。
- `server status` の状態が「クラッシュ」なら start コマンド自体を worktree 内で手で
  実行して原因を確認する。作業ディレクトリは `<worktree>/<dir>`（`dir` は
  **worktree 内の相対パス**。絶対パスや `repos_dir` 側のパスではない）。
- ポート衝突で落ちる場合は `on_activate = "lsof -ti:<port> | xargs -r kill"` のような
  前処理を検討する。

### list に `(!)` が付く / 壊れた worktree

元リポジトリの削除・作り直しなどで gitdir ポインタが死んだチェックアウト。
`worktree-integrator doctor --fix` で修復（prune）できる。worktree そのものを
片付けたい場合は `worktree-integrator remove <name>`（未コミット変更があると拒否。
CLI の `--force` で上書き可、MCP には force が無い）。

### 状態がおかしくなったときの最終手段

まず `doctor --fix`。それでも解消しない場合のみ、稼働中サーバーを
`worktree-integrator server stop` で全停止してから
`~/.local/state/worktree-integrator/` 配下（`servers.toml` / `aliases.toml` / `logs/`）
の削除を検討する（別名も消えるので、消す前にユーザーへ確認する）。
