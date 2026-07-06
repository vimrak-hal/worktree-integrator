package git_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vimrak-hal/worktree-integrator/internal/core/git"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/testutil"
)

func TestIsWorkTree(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "main")

	// クローンしたリポジトリは .git エントリを持つためワーキングツリー。
	if !git.IsWorkTree(repoPath) {
		t.Errorf("IsWorkTree(clone) = false, want true")
	}
	// 空ディレクトリは .git を持たないためワーキングツリーではない。
	empty := t.TempDir()
	if git.IsWorkTree(empty) {
		t.Errorf("IsWorkTree(empty) = true, want false")
	}
	// 存在しないパスも false。
	if git.IsWorkTree(filepath.Join(tmp, "does-not-exist")) {
		t.Errorf("IsWorkTree(missing) = true, want false")
	}
}

func TestFetchRef(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "main")

	// クローン済みリポジトリは origin が設定済みなので fetch は成功する。
	if err := git.FetchRef(t.Context(), repoPath, "origin", "main"); err != nil {
		t.Fatalf("FetchRef(origin, main) error = %v", err)
	}
}

func TestFetchRefUnknownRemoteFails(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "main")

	// 未設定のリモートを fetch しようとするとエラーになる。
	if err := git.FetchRef(t.Context(), repoPath, "nope", "main"); err == nil {
		t.Fatal("FetchRef(nope) error = nil, want error")
	}
}

// ctx のキャンセルは、ブロックしている fetch を中断させる。ハングするリモートは
// git-remote-ext の「ext::<command>」形式で作る: git はコマンド（ここでは sleep）を
// リモートヘルパーとして起動し、その応答を待ち続けるため、fetch は sleep の長さだけ
// ブロックする。ext トランスポートは既定で無効なので GIT_ALLOW_PROTOCOL で許可する
// （git.FetchRef は os.Environ() を引き継ぐため t.Setenv が届く）。
func TestFetchRefIsInterruptedByCancel(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "main")
	t.Setenv("GIT_ALLOW_PROTOCOL", "ext")

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		// キャンセルされなければ 30 秒ブロックする fetch。
		done <- git.FetchRef(ctx, repoPath, "ext::sleep 30", "main")
	}()

	time.Sleep(200 * time.Millisecond) // fetch がブロックに入るまで少し待つ
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("FetchRef error = %v, want context.Canceled", err)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("FetchRef was not interrupted promptly (took %v)", elapsed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("FetchRef did not return after cancel")
	}
}

// キャンセル済みの ctx では、git を実行する前に即座に失敗する。
func TestFetchRefRejectsCanceledContext(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "main")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := git.FetchRef(ctx, repoPath, "origin", "main"); !errors.Is(err, context.Canceled) {
		t.Fatalf("FetchRef(canceled) error = %v, want context.Canceled", err)
	}
}

// DefaultBranch は、クローン時に設定される refs/remotes/<remote>/HEAD の
// symbolic-ref をまず見る（(g) の検証要件: symbolic-ref あり）。
func TestDefaultBranchUsesSymbolicRef(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "main")

	branch, err := git.DefaultBranch(t.Context(), repoPath, "origin")
	if err != nil {
		t.Fatalf("DefaultBranch error = %v", err)
	}
	if branch != "main" {
		t.Fatalf("DefaultBranch = %q, want main (via symbolic-ref)", branch)
	}
}

// symbolic-ref が無い場合は main の存在確認にフォールバックする（(g) の検証要件:
// symbolic-ref なし・main）。
func TestDefaultBranchFallsBackToMainWithoutSymbolicRef(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "main")
	testutil.Git(t, repoPath, "symbolic-ref", "-d", "refs/remotes/origin/HEAD")

	branch, err := git.DefaultBranch(t.Context(), repoPath, "origin")
	if err != nil {
		t.Fatalf("DefaultBranch error = %v", err)
	}
	if branch != "main" {
		t.Fatalf("DefaultBranch = %q, want main (via existence fallback)", branch)
	}
}

// main が無ければ master にフォールバックする（(g) の検証要件: master）。
func TestDefaultBranchFallsBackToMaster(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "master")
	testutil.Git(t, repoPath, "symbolic-ref", "-d", "refs/remotes/origin/HEAD")

	branch, err := git.DefaultBranch(t.Context(), repoPath, "origin")
	if err != nil {
		t.Fatalf("DefaultBranch error = %v", err)
	}
	if branch != "master" {
		t.Fatalf("DefaultBranch = %q, want master", branch)
	}
}

// main も master も symbolic-ref も無ければエラーになる。
func TestDefaultBranchNoMainOrMaster(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "develop")
	testutil.Git(t, repoPath, "symbolic-ref", "-d", "refs/remotes/origin/HEAD")

	if _, err := git.DefaultBranch(t.Context(), repoPath, "origin"); err == nil {
		t.Fatal("DefaultBranch error = nil, want error")
	}
}

func TestResolveTip(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "main")

	tip, err := git.ResolveTip(t.Context(), repoPath, "origin", "main")
	if err != nil {
		t.Fatalf("ResolveTip error = %v", err)
	}
	// origin/main の HEAD コミットと一致するハッシュが返る。
	want := revParse(t, repoPath, "refs/remotes/origin/main")
	if tip != want {
		t.Fatalf("ResolveTip = %q, want %q", tip, want)
	}
}

func TestResolveTipUnknownBranchFails(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "main")
	if _, err := git.ResolveTip(t.Context(), repoPath, "origin", "no-such-branch"); err == nil {
		t.Fatal("ResolveTip(no-such-branch) error = nil, want error")
	}
}

func TestRemoteBranchExists(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "main")

	ok, err := git.RemoteBranchExists(t.Context(), repoPath, "origin", "main")
	if err != nil || !ok {
		t.Fatalf("RemoteBranchExists(main) = %v, %v, want true, nil", ok, err)
	}
	ok, err = git.RemoteBranchExists(t.Context(), repoPath, "origin", "no-such-branch")
	if err != nil || ok {
		t.Fatalf("RemoteBranchExists(missing) = %v, %v, want false, nil", ok, err)
	}
}

func TestLocalBranchExists(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "main")

	// クローン直後の既定ブランチ main は存在する。
	ok, err := git.LocalBranchExists(t.Context(), repoPath, "main")
	if err != nil {
		t.Fatalf("LocalBranchExists(main) error = %v", err)
	}
	if !ok {
		t.Error("LocalBranchExists(main) = false, want true")
	}

	// 存在しないブランチ名では false（エラーではない）。
	ok, err = git.LocalBranchExists(t.Context(), repoPath, "no-such-branch")
	if err != nil {
		t.Fatalf("LocalBranchExists(missing) error = %v", err)
	}
	if ok {
		t.Error("LocalBranchExists(missing) = true, want false")
	}

	// 後から作成したブランチも検出できる。
	testutil.Git(t, repoPath, "branch", "added")
	ok, err = git.LocalBranchExists(t.Context(), repoPath, "added")
	if err != nil {
		t.Fatalf("LocalBranchExists(added) error = %v", err)
	}
	if !ok {
		t.Error("LocalBranchExists(added) = false, want true")
	}
}

func TestAddWorktree(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "main")
	target := filepath.Join(tmp, "wt", "repo")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}

	start := revParse(t, repoPath, "refs/remotes/origin/main")
	if err := git.AddWorktree(t.Context(), repoPath, "feature-x", target, start); err != nil {
		t.Fatalf("AddWorktree error = %v", err)
	}

	// 連結ワークツリーの .git ファイルが作られ、チェックアウト内容が存在する。
	if _, err := os.Stat(filepath.Join(target, ".git")); err != nil {
		t.Fatal("worktree gitfile missing")
	}
	if _, err := os.Stat(filepath.Join(target, "README.md")); err != nil {
		t.Fatal("checkout content missing")
	}
	// -b で渡したブランチが新規作成される。
	ok, err := git.LocalBranchExists(t.Context(), repoPath, "feature-x")
	if err != nil {
		t.Fatalf("LocalBranchExists error = %v", err)
	}
	if !ok {
		t.Fatal("branch feature-x missing after AddWorktree")
	}
}

func TestAddWorktreeSlashedBranch(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "main")
	target := filepath.Join(tmp, "wt", "feat", "sub", "repo")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}

	start := revParse(t, repoPath, "refs/remotes/origin/main")
	if err := git.AddWorktree(t.Context(), repoPath, "feat/sub-x", target, start); err != nil {
		t.Fatalf("AddWorktree error = %v", err)
	}
	// スラッシュを含む完全なブランチ名が保持される。
	ok, _ := git.LocalBranchExists(t.Context(), repoPath, "feat/sub-x")
	if !ok {
		t.Fatal("slashed branch feat/sub-x missing")
	}
}

func TestAddWorktreeExistingBranchFails(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "main")
	target := filepath.Join(tmp, "wt", "repo")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	testutil.Git(t, repoPath, "branch", "dup")

	start := revParse(t, repoPath, "refs/remotes/origin/main")
	// -b で既存ブランチ名を作ろうとすると git が失敗する。
	if err := git.AddWorktree(t.Context(), repoPath, "dup", target, start); err == nil {
		t.Fatal("AddWorktree(dup) error = nil, want error")
	}
}

func TestIgnoredPaths(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "main")

	// 単一ファイルと、まとめて無視されるディレクトリを用意する。
	if err := os.WriteFile(filepath.Join(repoPath, ".gitignore"), []byte("ignored.txt\nnode_modules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoPath, "ignored.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repoPath, "node_modules", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoPath, "node_modules", "pkg", "dep.js"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	paths, err := git.IgnoredPaths(t.Context(), repoPath)
	if err != nil {
		t.Fatalf("IgnoredPaths error = %v", err)
	}

	// 無視エントリが相対パスで返り、ディレクトリは末尾スラッシュ無しでまとめられる。
	if !slices.Contains(paths, "ignored.txt") {
		t.Errorf("IgnoredPaths = %v, want to contain %q", paths, "ignored.txt")
	}
	if !slices.Contains(paths, "node_modules") {
		t.Errorf("IgnoredPaths = %v, want to contain %q (no trailing slash)", paths, "node_modules")
	}
	// まとめられているので、配下の個別ファイルは列挙されない。
	if slices.Contains(paths, "node_modules/pkg/dep.js") {
		t.Errorf("IgnoredPaths = %v, must not enumerate files under collapsed dir", paths)
	}
}

func TestIgnoredPathsEmpty(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "main")

	// 無視対象が無いリポジトリでは空スライスが返り、エラーにならない。
	paths, err := git.IgnoredPaths(t.Context(), repoPath)
	if err != nil {
		t.Fatalf("IgnoredPaths error = %v", err)
	}
	if len(paths) != 0 {
		t.Errorf("IgnoredPaths = %v, want empty", paths)
	}
}

func TestIgnoredPathsNonRepoFails(t *testing.T) {
	// git 管理下にないディレクトリでは ls-files が失敗し、エラーが伝播する。
	if _, err := git.IgnoredPaths(t.Context(), t.TempDir()); err == nil {
		t.Fatal("IgnoredPaths(non-repo) error = nil, want error")
	}
}

// revParse は dir のリポジトリで ref を解決し、コミットハッシュを返すテストヘルパー。
// グローバル／システムの git 設定から隔離して実行する。
func revParse(t *testing.T, dir, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--verify", ref+"^{commit}")
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse %s failed: %v", ref, err)
	}
	return strings.TrimSpace(string(out))
}
