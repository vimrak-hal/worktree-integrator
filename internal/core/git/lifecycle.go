package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// PruneWorktrees は作業ツリーが失われた古い連結ワークツリーのメタデータ
// （例: `git worktree remove` ではなく `rm -rf` で削除されたディレクトリ）を
// 削除する。これにより、古い管理情報と衝突せずに名前を再作成できる。
// 生きているワークツリーには一切手を触れない。
func PruneWorktrees(ctx context.Context, dir string) error {
	_, err := run(ctx, dir, "worktree", "prune")
	return err
}

// PruneWorktreesDryRun は PruneWorktrees が削除する対象の報告（`git worktree prune
// --dry-run --verbose` の出力）を、何も削除せずに返す。空文字列は「掃除すべき
// メタデータが無い」を意味する。doctor が --fix なしで発見を報告するために使う。
//
// git はこの dry-run 報告を（stdout ではなく）stderr に書くため、共通の run では
// なく stderr を戻り値として取り込む専用の実行を行う。
func PruneWorktreesDryRun(ctx context.Context, dir string) (string, error) {
	args := []string{"worktree", "prune", "--dry-run", "--verbose"}
	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.WaitDelay = waitDelay
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), ctxErr)
		}
		return "", gitError(args, stderr.String(), err)
	}
	return strings.TrimRight(stderr.String(), "\n"), nil
}

// RemoveWorktree は dir のリポジトリから target の連結ワークツリーを削除する
// （作業ディレクトリと管理メタデータの両方）。未コミットの変更が残っている場合、
// git は削除を拒否してエラーになる（意図的な安全弁）。force はその拒否を
// `--force` で上書きする。ブランチは削除しない（DeleteBranch が別途担う）。
func RemoveWorktree(ctx context.Context, dir, target string, force bool) error {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, target)
	_, err := run(ctx, dir, args...)
	return err
}

// DeleteBranch は dir のリポジトリから refs/heads/<branch> を強制削除する
// （`git branch -D`。マージ済みかは問わない — worktree ライフサイクルの後始末は
// 「作った名前を消す」ことが目的で、残すかどうかの判断はユーザーが --keep-branch で
// 行う）。ブランチがどこかのワークツリーでチェックアウトされたままなら git が
// 拒否してエラーになる。
func DeleteBranch(ctx context.Context, dir, branch string) error {
	_, err := run(ctx, dir, "branch", "-D", branch)
	return err
}

// CurrentBranch は dir でチェックアウトされているブランチ名を返す（detached HEAD
// では "HEAD"）。gitdir ポインタが壊れている（rm -rf 残骸などで実体を失った）
// ワークツリーではエラーになる — inventory はそれを Healthy=false として扱う。
func CurrentBranch(ctx context.Context, dir string) (string, error) {
	return run(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
}

// HasGitDir は dir の ".git" エントリが実際に有効な git ディレクトリ（または有効な
// gitdir ポインタ）かを返す。".git" というエントリが存在するだけの「名ばかり
// リポジトリ」の検出（doctor）に使う。`git rev-parse --git-dir` ではなく
// `--resolve-git-dir` を使うのは、前者が無効な .git を黙ってスキップして上位
// ディレクトリのリポジトリを報告してしまう（偽陰性）のに対し、後者は与えた
// エントリ自体を検証するためである。判定できなかった場合（git を実行できない・
// キャンセル）のみエラーを返す。
func HasGitDir(ctx context.Context, dir string) (bool, error) {
	return succeeds(ctx, dir, "rev-parse", "--resolve-git-dir", filepath.Join(dir, ".git"))
}
