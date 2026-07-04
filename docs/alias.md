# worktree の別名（`alias` サブコマンド）

worktree に**人間向けの表示名（別名）**を付け、`server status` の ALIAS 列に表示できます。
たとえば worktree 名 `ABC-123` に Jira チケットのタイトルを別名として登録しておくと、
一覧でどの worktree が何の作業かが一目で分かります。

```sh
worktree-integrator alias set <worktree名> <表示名>   # 設定・更新（空文字で削除）
worktree-integrator alias list                        # 一覧（別名 ls）
worktree-integrator alias get <worktree名>            # 値だけ出力（未設定なら何も出さない）
worktree-integrator alias rm  <worktree名>            # 削除
```

別名は `worktree名 → 表示名` の 1 対 1 マップとして、状態ディレクトリの単一ファイル
`$XDG_STATE_HOME/worktree-integrator/aliases.toml`（未設定時は
`~/.local/state/worktree-integrator/aliases.toml`）に保存されます。書き込みは
`servers.toml` と同じく排他ロック下でアトミック（一時ファイル＋ rename）に行われ、表示名は
先頭 1 行・トリム済みに正規化されます（一覧の桁ずれ防止）。worktree 名は `create` と同じく
英数字とハイフンのみが有効です。

別名はフックから登録するのが便利です。たとえば `before` フックで Jira のタイトルを取得し、
`worktree-integrator alias set "$WT_WORKTREE_NAME" "<タイトル>"` を呼ぶだけです（`create` 自体は
状態を持たず、別名の永続化はこの明示的な `alias set` 呼び出しが担います）。実例は
[`examples/hooks/cmux-jira-title.sh`](https://github.com/vimrak-hal/worktree-integrator/blob/main/examples/hooks/cmux-jira-title.sh)
を参照してください。
