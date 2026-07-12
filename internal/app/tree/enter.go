package tree

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/vimrak-hal/worktree-integrator/internal/app/action"
	"github.com/vimrak-hal/worktree-integrator/internal/app/create"
	"github.com/vimrak-hal/worktree-integrator/internal/core/hooks"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/wtenv"
)

// EnterResult は `enter` の結果。
type EnterResult struct {
	// Worktree は遷移先の worktree 名。
	Worktree string `json:"worktree"`
	// Root は worktree のルートディレクトリ。
	Root string `json:"root"`
	// Hooks は実行された after フックの結果。
	Hooks []create.HookOutcome `json:"hooks,omitempty"`
}

// Enter は既存の worktree への遷移を表す明示コマンドである: ルートの存在を検証し、
// after フックだけを実行する。旧 create の「ルート存在で全スキップ + after フック
// のみ」というショートサーキットの実挙動をコマンドとして切り出したもので、cmux の
// ナビゲーションフックのような遷移運用はこれで従来と同じ体感を維持できる。
//
// 専用の on_enter フックタイミングは意図的に追加しない（設計判断）: after は
// 「作成完了 / 遷移」フックであり、create の完了時と enter の両方で実行されるのが
// 仕様である。before / after_worktree は実行されない（何も作らないため）。
func Enter(ctx context.Context, d Deps, name action.Name) (*EnterResult, error) {
	root := filepath.Join(d.WorktreesDir, name.String())
	if !isDir(root) {
		return nil, fmt.Errorf("worktree %q がありません（%s）", name.String(), root)
	}
	runCtx := wtenv.NewRunContext(name.String(), d.ReposDir, d.WorktreesDir)
	res := &EnterResult{Worktree: name.String(), Root: runCtx.Root}
	after := hooks.Run(ctx, d.Config.Hooks.After, runCtx, d.ChildIO)
	res.Hooks = create.HookOutcomes("after", after)
	if hooks.AnyFatal(after) {
		return res, errors.New("1 つ以上のフックが失敗しました")
	}
	return res, nil
}
