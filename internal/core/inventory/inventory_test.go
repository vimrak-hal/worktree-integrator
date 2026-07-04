package inventory

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vimrak-hal/worktree-integrator/internal/core/git"
	"github.com/vimrak-hal/worktree-integrator/internal/core/git/repo"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/testutil"
)

// addWorktree は repoPath のリポジトリから target へ branch という名前の連結
// ワークツリーを作る。
func addWorktree(t *testing.T, repoPath, branch, target string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	testutil.Git(t, repoPath, "worktree", "add", "-b", branch, target, "HEAD")
}

// 正常系: 各 worktree セットとそのメンバー（ブランチ・健全性）が列挙される。
func TestScanListsWorktreesWithMembers(t *testing.T) {
	reposDir := t.TempDir()
	worktreesDir := t.TempDir()
	repoA := testutil.CloneWithBranchNamed(t, reposDir, "main", "repo-a")
	repoB := testutil.CloneWithBranchNamed(t, reposDir, "main", "repo-b")
	addWorktree(t, repoA, "feat-x", filepath.Join(worktreesDir, "feat-x", "repo-a"))
	addWorktree(t, repoB, "feat-x-b", filepath.Join(worktreesDir, "feat-x", "repo-b"))
	addWorktree(t, repoA, "fix-y", filepath.Join(worktreesDir, "fix-y", "repo-a"))

	known := []repo.Repo{{Name: "repo-a", Path: repoA}, {Name: "repo-b", Path: repoB}}
	got, err := Scan(t.Context(), worktreesDir, known)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("worktrees = %+v", got)
	}
	featX := got[0]
	if featX.Name != "feat-x" || featX.Root != filepath.Join(worktreesDir, "feat-x") {
		t.Fatalf("featX = %+v", featX)
	}
	if len(featX.Repos) != 2 {
		t.Fatalf("featX repos = %+v", featX.Repos)
	}
	if r := featX.Repos[0]; r.Repo != "repo-a" || r.Branch != "feat-x" || !r.Healthy {
		t.Fatalf("repo-a entry = %+v", r)
	}
	if r := featX.Repos[1]; r.Repo != "repo-b" || r.Branch != "feat-x-b" || !r.Healthy {
		t.Fatalf("repo-b entry = %+v", r)
	}
	if featX.Broken() {
		t.Fatal("healthy worktree must not be broken")
	}
	if got[1].Name != "fix-y" || len(got[1].Repos) != 1 {
		t.Fatalf("fix-y = %+v", got[1])
	}
}

// 壊れた gitdir: ソースリポジトリ側の worktree メタデータを消すと（rm -rf 残骸の
// 再現）、そのメンバーは Healthy=false・Branch 空として報告される。
func TestScanDetectsBrokenGitdir(t *testing.T) {
	reposDir := t.TempDir()
	worktreesDir := t.TempDir()
	repoA := testutil.CloneWithBranchNamed(t, reposDir, "main", "repo-a")
	target := filepath.Join(worktreesDir, "feat-x", "repo-a")
	addWorktree(t, repoA, "feat-x", target)

	// gitdir ポインタの指す先（<repo>/.git/worktrees）を破壊する。
	if err := os.RemoveAll(filepath.Join(repoA, ".git", "worktrees")); err != nil {
		t.Fatal(err)
	}

	got, err := Scan(t.Context(), worktreesDir, []repo.Repo{{Name: "repo-a", Path: repoA}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[0].Repos) != 1 {
		t.Fatalf("got = %+v", got)
	}
	entry := got[0].Repos[0]
	if entry.Healthy || entry.Branch != "" {
		t.Fatalf("entry = %+v, want unhealthy with empty branch", entry)
	}
	if !got[0].Broken() {
		t.Fatal("worktree with a broken member should report Broken")
	}
}

// worktrees_dir 直下の空ディレクトリは、リポジトリ 0 件の worktree セットとして
// 報告される（作成途中・削除残骸の可視化）。
func TestScanReportsEmptyDirectoryAsEmptyWorktree(t *testing.T) {
	worktreesDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(worktreesDir, "leftover"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := Scan(t.Context(), worktreesDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "leftover" || len(got[0].Repos) != 0 {
		t.Fatalf("got = %+v", got)
	}
}

// 不存在の worktrees_dir は空リストで正常（初回利用はエラーではない）。
func TestScanMissingWorktreesDirIsEmpty(t *testing.T) {
	got, err := Scan(t.Context(), filepath.Join(t.TempDir(), "does-not-exist"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got = %+v", got)
	}
}

// '/' を含む worktree 名（feature/login）はネストしたディレクトリに作られるため、
// スキャンは再帰して相対パスを名前として報告する。
func TestScanFindsNestedWorktreeNames(t *testing.T) {
	reposDir := t.TempDir()
	worktreesDir := t.TempDir()
	repoA := testutil.CloneWithBranchNamed(t, reposDir, "main", "repo-a")
	addWorktree(t, repoA, "feature/login", filepath.Join(worktreesDir, "feature", "login", "repo-a"))

	got, err := Scan(t.Context(), worktreesDir, []repo.Repo{{Name: "repo-a", Path: repoA}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "feature/login" {
		t.Fatalf("got = %+v", got)
	}
	if len(got[0].Repos) != 1 || got[0].Repos[0].Branch != "feature/login" {
		t.Fatalf("repos = %+v", got[0].Repos)
	}
}

// Members は 1 つのルート直下の .git を持つサブディレクトリだけを列挙する
// （.git の無いサブディレクトリ・通常ファイルは無視）。
func TestMembersListsOnlyCheckouts(t *testing.T) {
	reposDir := t.TempDir()
	root := t.TempDir()
	repoA := testutil.CloneWithBranchNamed(t, reposDir, "main", "repo-a")
	addWorktree(t, repoA, "feat-m", filepath.Join(root, "repo-a"))
	if err := os.MkdirAll(filepath.Join(root, "not-a-repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	members, err := Members(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0].Repo != "repo-a" || !members[0].Healthy {
		t.Fatalf("members = %+v", members)
	}
	if !git.IsWorkTree(members[0].Path) {
		t.Fatalf("member path %s should be a worktree", members[0].Path)
	}
}
