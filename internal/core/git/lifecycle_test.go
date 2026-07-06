package git_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vimrak-hal/worktree-integrator/internal/core/git"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/testutil"
)

// RemoveWorktree はクリーンなチェックアウトを削除する。dirty なチェックアウトは
// git の安全弁で拒否され、force で上書きできる。
func TestRemoveWorktreeCleanAndDirty(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "main")
	target := filepath.Join(t.TempDir(), "feat")
	testutil.Git(t, repoPath, "worktree", "add", "-b", "feat", target, "HEAD")

	// dirty にする → 拒否。
	if err := os.WriteFile(filepath.Join(target, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := git.RemoveWorktree(t.Context(), repoPath, target, false); err == nil {
		t.Fatal("dirty worktree removal should be refused")
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal("refused removal must leave the worktree in place")
	}

	// force で削除できる。
	if err := git.RemoveWorktree(t.Context(), repoPath, target, true); err != nil {
		t.Fatalf("forced removal failed: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("worktree should be gone: %v", err)
	}
}

// DeleteBranch はブランチを強制削除する。チェックアウト中のブランチは git が拒否する。
func TestDeleteBranch(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "main")
	target := filepath.Join(t.TempDir(), "feat")
	testutil.Git(t, repoPath, "worktree", "add", "-b", "feat", target, "HEAD")

	// チェックアウト中は拒否される。
	if err := git.DeleteBranch(t.Context(), repoPath, "feat"); err == nil {
		t.Fatal("deleting a checked-out branch should be refused")
	}

	if err := git.RemoveWorktree(t.Context(), repoPath, target, false); err != nil {
		t.Fatal(err)
	}
	if err := git.DeleteBranch(t.Context(), repoPath, "feat"); err != nil {
		t.Fatalf("DeleteBranch failed: %v", err)
	}
	exists, err := git.LocalBranchExists(t.Context(), repoPath, "feat")
	if err != nil || exists {
		t.Fatalf("branch should be gone: exists=%v err=%v", exists, err)
	}
}

// CurrentBranch はチェックアウト中のブランチ名を返す。
func TestCurrentBranch(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "main")
	if branch, err := git.CurrentBranch(t.Context(), repoPath); err != nil || branch != "main" {
		t.Fatalf("CurrentBranch = %q, %v", branch, err)
	}
	target := filepath.Join(t.TempDir(), "feat")
	testutil.Git(t, repoPath, "worktree", "add", "-b", "feature/login", target, "HEAD")
	if branch, err := git.CurrentBranch(t.Context(), target); err != nil || branch != "feature/login" {
		t.Fatalf("CurrentBranch(worktree) = %q, %v", branch, err)
	}
}

func TestPruneWorktrees(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "main")

	// 連結ワークツリーを作り、ディレクトリだけ手動削除して孤立メタデータを残す。
	orphan := filepath.Join(tmp, "oldwt", "repo")
	if err := os.MkdirAll(filepath.Dir(orphan), 0o755); err != nil {
		t.Fatal(err)
	}
	testutil.Git(t, repoPath, "worktree", "add", "-b", "throwaway", orphan)
	if err := os.RemoveAll(orphan); err != nil {
		t.Fatal(err)
	}

	// prune 前は孤立した管理情報がまだ列挙される。
	metaDir := filepath.Join(repoPath, ".git", "worktrees", "repo")
	if _, err := os.Stat(metaDir); err != nil {
		t.Fatalf("expected orphaned worktree metadata at %s: %v", metaDir, err)
	}

	if err := git.PruneWorktrees(t.Context(), repoPath); err != nil {
		t.Fatalf("PruneWorktrees error = %v", err)
	}

	// prune 後は孤立した管理情報が削除されている。
	if _, err := os.Stat(metaDir); !os.IsNotExist(err) {
		t.Fatalf("orphaned metadata still present after prune: err=%v", err)
	}
}

func TestPruneWorktreesKeepsLiveWorktree(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "main")

	live := filepath.Join(tmp, "livewt", "repo")
	if err := os.MkdirAll(filepath.Dir(live), 0o755); err != nil {
		t.Fatal(err)
	}
	testutil.Git(t, repoPath, "worktree", "add", "-b", "alive", live)

	if err := git.PruneWorktrees(t.Context(), repoPath); err != nil {
		t.Fatalf("PruneWorktrees error = %v", err)
	}
	// 生きているワークツリーには手を触れない。
	if _, err := os.Stat(filepath.Join(live, ".git")); err != nil {
		t.Fatal("live worktree must survive prune")
	}
}

// PruneWorktreesDryRun は rm -rf された worktree の残骸メタデータを（削除せずに）
// 報告し、PruneWorktrees が実際に掃除する。
func TestPruneWorktreesDryRunReportsResidue(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "main")
	target := filepath.Join(t.TempDir(), "feat")
	testutil.Git(t, repoPath, "worktree", "add", "-b", "feat", target, "HEAD")

	// 掃除すべきものが無ければ空。
	if out, err := git.PruneWorktreesDryRun(t.Context(), repoPath); err != nil || strings.TrimSpace(out) != "" {
		t.Fatalf("clean repo dry-run = %q, %v", out, err)
	}

	// rm -rf の残骸を作る。
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	out, err := git.PruneWorktreesDryRun(t.Context(), repoPath)
	if err != nil || strings.TrimSpace(out) == "" {
		t.Fatalf("dry-run should report the residue: %q, %v", out, err)
	}
	// dry-run は削除しない: もう一度実行しても報告が残る。
	if out2, err := git.PruneWorktreesDryRun(t.Context(), repoPath); err != nil || strings.TrimSpace(out2) == "" {
		t.Fatalf("dry-run must not prune: %q, %v", out2, err)
	}
	if err := git.PruneWorktrees(t.Context(), repoPath); err != nil {
		t.Fatal(err)
	}
	if out, err := git.PruneWorktreesDryRun(t.Context(), repoPath); err != nil || strings.TrimSpace(out) != "" {
		t.Fatalf("after prune the dry-run should be empty: %q, %v", out, err)
	}
}

// HasGitDir は本物のリポジトリ・連結ワークツリーで true、「名ばかり .git」で false。
// 上位ディレクトリのリポジトリに惑わされない（--resolve-git-dir を使う理由）。
func TestHasGitDir(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "main")
	if ok, err := git.HasGitDir(t.Context(), repoPath); err != nil || !ok {
		t.Fatalf("HasGitDir(clone) = %v, %v", ok, err)
	}

	target := filepath.Join(t.TempDir(), "feat")
	testutil.Git(t, repoPath, "worktree", "add", "-b", "feat", target, "HEAD")
	if ok, err := git.HasGitDir(t.Context(), target); err != nil || !ok {
		t.Fatalf("HasGitDir(linked worktree) = %v, %v", ok, err)
	}

	// リポジトリの内側に「名ばかり .git」ディレクトリを作っても、上位の本物を
	// 報告せず false になる。
	fake := filepath.Join(repoPath, "fake")
	if err := os.MkdirAll(filepath.Join(fake, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if ok, err := git.HasGitDir(t.Context(), fake); err != nil || ok {
		t.Fatalf("HasGitDir(fake .git) = %v, %v", ok, err)
	}
}
