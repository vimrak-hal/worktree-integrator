# ディレクトリ構成・設計

Go の標準的なレイアウト（`cmd/` + `internal/`）に従い、責務ごとにパッケージを分割
しています。

`internal/` 配下はレイヤー／責務でグルーピングしています。外部インターフェースの
**adapter**、ワークフローを束ねる **app**（アプリケーション層）、ドメインの **core**、
技術的な横断プリミティブの **infra** に分け、サブシステム固有の関心事は所有する
パッケージの下へさらにネストします（例: `core/git/worktree`）。依存は
`adapter → app → core → infra` の一方向で、下位の層が上位の層を import することは
ありません。設定（config）は core ドメイン型を組み立てつつ core(action) からも参照
される双方向の関心事なので、独立レイヤーではなく `core/config` に置いています。

```
cmd/worktree-integrator/   エントリポイント。引数解析の振り分けと終了コードの配線のみ
internal/
  ── adapter（外部インターフェース）──
  adapter/
    cli/        コマンドライン解析（cobra）。フラグを action.Overrides にして action へ渡し、対話的なリポジトリ選択（survey）も所有する
    mcpserver/  MCP サーバー（stdio）。ツール定義と app オーケストレーションへの橋渡し
  ── app（アプリケーション層）──
  app/          解決済み Action を各ワークフローへ振り分けるルーター（app.Run）。配下のワークフローを結線する
    create/     worktree 作成ワークフロー（探索 → 選択 → 並列作成 → フック → サマリ）と計画・選択、repo 一覧（ListRepos）。対話的選択は Selector で注入
    server/     サーバーライフサイクルのワークフロー（switch / status / stop / logs）。core/server を coreserver として駆動
    alias/      worktree 表示別名の設定・一覧・取得・削除ワークフロー。core/alias を corealias として駆動
    output/     ユーザー向け整形（並列進捗レポーター・最終サマリ・フック結果・server 切替/停止/状態の文言と表・repo 一覧）。app 配下だけが利用
  ── core（ドメイン）──
  core/
    action/     解決済みコマンド語彙（Action / Config / ServerCommand / AliasCommand）と、Overrides＋
                設定ファイルからの解決（優先順位）・ValidateName。両フロントエンドが共有する
    alias/      worktree 表示別名ストア（worktree 名→ラベル。server status の ALIAS 列）。server と状態ディレクトリを共有する peer で worktree 作成処理には依存しない
    config/     ~/.config/worktree-integrator.toml の読み込みと検証（hooks / server / copy スキーマを組み立てる）
    git/        ローカル git コマンドの薄いラッパー（fetch / rev-parse / worktree add・prune / ls-files）と
                「.git を持つ作業ツリーか」の判定。git の責務をこのサブツリーに集約。配下が共有する
      repo/       ~/repositories 配下の Git リポジトリ検出（対話的選択は adapter/cli が所有）
      worktree/   1 リポジトリ分の処理（fetch → worktree 作成）と並列実行・並列度決定・進捗
    hooks/      フック定義・結果型・致命判定（AnyFatal）と、タイミング単位の並列実行（結果の整形は app/output）
    server/     サーバー設定スキーマ・プロセス制御・切替/停止/状態ロジック
      serverfake/ テスト用のサーバープロセス フェイク
  ── infra（横断プリミティブ）──
  infra/
    store/      ロック付き・アトミック・バージョン付き TOML 永続化プリミティブ（server 状態と alias が共有）
    shellcmd/   設定上のコマンド（文字列 or 配列）→ sh -c スクリプトへの共有プリミティブ
    wtenv/      run/repo コンテキストと WT_* 環境変数の単一の真実
    childio/    生成する子プロセスの標準ストリームの接続先（CLI は端末、MCP は stderr/devnull）
    filecopy/   gitignore 等の追加ファイルを worktree へコピー（シンボリックリンク安全）
    testutil/   テスト用のローカル Git リポジトリ生成ヘルパー
```

- `main` は引数を `cli.Parse` で `Action` に解決し、`app.Run`（または MCP）へ振り分ける
  だけの薄いラッパーです。対話的な選択処理は `Selector` 関数型で差し替え可能なため、
  オーケストレーションを TTY なしで単体テストできます。
- ストア（`server` 状態と `alias`）は共有の `store.File[T]`（ジェネリック）に委譲し、
  排他アドバイザリロック（flock）＋一時ファイル＋ rename のアトミック書き込みを共通化
  しています。
- `internal/` 配下は外部から import できないため、公開面は `cmd` の薄いエントリと、
  MCP・テストから再利用される `app` のオーケストレーション関数に限られます。
