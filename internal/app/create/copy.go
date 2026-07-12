package create

import (
	"context"
	"slices"

	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
	"github.com/vimrak-hal/worktree-integrator/internal/core/git"
	"github.com/vimrak-hal/worktree-integrator/internal/core/git/worktree"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/fscopy"
)

// copyExtras は、新規作成されたチェックアウトへ設定された追加ファイルをコピーする
// post-create ステップである（かつては worktree.Process に埋まっていたが、worktree
// パッケージを git 操作の単一責務に保つためここへ移した）。
//
// 2 段階で行う。まず明示的なパス（常にコピーされる）、次に — gitignored が
// 有効な場合 — ソースリポジトリが報告する gitignore された全エントリから、
// ユーザーの除外指定と既に明示的にコピーしたものを差し引いたもの。
// 設計上ベストエフォートであり、失敗は Report（と reporter への型付きイベント）で
// 報告されるが worktree の作成自体は失敗させない。プランが空なら nil を返す。
func copyExtras(ctx context.Context, repoPath, target, repoName string, plan config.CopyPlan, reporter worktree.Reporter) *fscopy.Report {
	if plan.IsEmpty() {
		return nil
	}

	var report fscopy.Report

	// 段階 1: 明示的なパス（gitignored の除外指定の対象にはならない）。
	if len(plan.Paths) > 0 {
		report.Merge(fscopy.CopyInto(ctx, repoPath, target, plan.Paths, nil))
	}

	// 段階 2: gitignore された全エントリから、既に処理した明示的なパスを差し引いたもの。
	if plan.Gitignored {
		ignored, err := git.IgnoredPaths(ctx, repoPath)
		if err != nil {
			reporter.Event(repoName, worktree.Note{Kind: worktree.NoteGitignoreListFailed, Err: err})
		} else {
			var selected []string
			for _, p := range ignored {
				if !slices.Contains(plan.Paths, p) {
					selected = append(selected, p)
				}
			}
			report.Merge(fscopy.CopyInto(ctx, repoPath, target, selected, plan.Exclude))
		}
	}

	for _, rel := range report.Rejected {
		reporter.Event(repoName, worktree.Note{Kind: worktree.NoteCopyRejected, Path: rel})
	}
	for _, f := range report.Failures {
		reporter.Event(repoName, worktree.Note{Kind: worktree.NoteCopyFailed, Path: f.Path, Err: f.Err})
	}
	return &report
}
