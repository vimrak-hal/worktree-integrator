package create

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vimrak-hal/worktree-integrator/internal/app/action"
	"github.com/vimrak-hal/worktree-integrator/internal/core/cmdspec"
	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
	"github.com/vimrak-hal/worktree-integrator/internal/core/git/repo"
	"github.com/vimrak-hal/worktree-integrator/internal/core/hooks"
	coreserver "github.com/vimrak-hal/worktree-integrator/internal/core/server"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/childio"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/statedir"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/testutil"
)

func noEnv(string) string { return "" }

// deps はテスト用の依存の束を構築する。Run は io.Writer に書かないため、テストは
// Result のフィールドのみを検証する。
func deps(t *testing.T, selector Selector) Deps {
	t.Helper()
	return depsAt(statedir.At(t.TempDir()), selector)
}

func depsAt(root statedir.Root, selector Selector) Deps {
	return Deps{
		ChildIO:  childio.Streams{Stdout: io.Discard, Stderr: io.Discard},
		Selector: selector,
		Root:     root,
	}
}

// cfgOf は対話選択モードの action.Create をテスト用に解決する。
func cfgOf(t *testing.T, name, reposDir, worktreesDir string) action.Create {
	t.Helper()
	cfg, err := action.NewCreate(name, nil, false, "", action.Overrides{
		ReposDir: reposDir, WorktreesDir: worktreesDir, Remote: "origin",
	}, &config.File{}, noEnv, os.UserHomeDir)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestNoRepositoriesFoundIsNotError(t *testing.T) {
	cfg := cfgOf(t, "feat", t.TempDir(), t.TempDir())
	res, err := Run(t.Context(), deps(t, func([]repo.Repo) ([]repo.Repo, error) { return nil, nil }), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Disposition != DispositionNoRepos {
		t.Fatalf("disposition = %q, want %q", res.Disposition, DispositionNoRepos)
	}
	if res.Discovered != 0 || res.ReposDir != cfg.ReposDir {
		t.Fatalf("res = %+v", res)
	}
}

func TestSelectingNothingDoesNothing(t *testing.T) {
	repos := t.TempDir()
	testutil.CloneWithBranchNamed(t, repos, "main", "repo-a")
	cfg := cfgOf(t, "feat", repos, t.TempDir())
	res, err := Run(t.Context(), deps(t, func([]repo.Repo) ([]repo.Repo, error) { return nil, nil }), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Disposition != DispositionNothingSelected {
		t.Fatalf("disposition = %q, want %q", res.Disposition, DispositionNothingSelected)
	}
	if res.Discovered != 1 {
		t.Fatalf("discovered = %d", res.Discovered)
	}
}

func TestEndToEndCreatesWorktreesAndSummarizes(t *testing.T) {
	repos := t.TempDir()
	worktrees := t.TempDir()
	testutil.CloneWithBranchNamed(t, repos, "main", "repo-a")
	testutil.CloneWithBranchNamed(t, repos, "master", "repo-b")

	cfg := cfgOf(t, "shared-feature", repos, worktrees)
	res, err := Run(t.Context(), deps(t, func(r []repo.Repo) ([]repo.Repo, error) { return r, nil }), cfg)
	if err != nil {
		t.Fatalf("err = %v\nres = %+v", err, res)
	}
	if res.Disposition != DispositionCreated || res.Created != 2 || res.Skipped != 0 || res.Failed != 0 {
		t.Fatalf("res = %+v", res)
	}
	if len(res.Repos) != 2 {
		t.Fatalf("repos = %+v", res.Repos)
	}
	for _, r := range res.Repos {
		if r.Status != RepoCreated {
			t.Fatalf("repo %s status = %q", r.Repo, r.Status)
		}
	}
	for _, name := range []string{"repo-a", "repo-b"} {
		if _, err := os.Stat(filepath.Join(worktrees, "shared-feature", name, ".git")); err != nil {
			t.Fatalf("missing worktree for %s", name)
		}
	}
}

// 対話選択モードの差分作成: 既にメンバーとして存在するリポジトリは選択肢に現れず、
// 未作成のリポジトリだけが提示される（旧「ルート存在で全スキップ」の全廃）。
func TestInteractiveModeOffersOnlyUncreatedRepos(t *testing.T) {
	repos := t.TempDir()
	worktrees := t.TempDir()
	testutil.CloneWithBranchNamed(t, repos, "main", "repo-a")
	testutil.CloneWithBranchNamed(t, repos, "main", "repo-b")

	// repo-a だけ先に作成しておく。
	cfg := cfgOf(t, "feat", repos, worktrees)
	cfg.Repos = []string{"repo-a"}
	if _, err := Run(t.Context(), deps(t, nil), cfg); err != nil {
		t.Fatal(err)
	}

	// 対話選択には repo-b のみが提示され、選択すれば作成される。
	var offered []string
	cfg = cfgOf(t, "feat", repos, worktrees)
	res, err := Run(t.Context(), deps(t, func(r []repo.Repo) ([]repo.Repo, error) {
		for _, x := range r {
			offered = append(offered, x.Name)
		}
		return r, nil
	}), cfg)
	if err != nil {
		t.Fatalf("err = %v\nres = %+v", err, res)
	}
	if len(offered) != 1 || offered[0] != "repo-b" {
		t.Fatalf("offered = %v, want [repo-b]", offered)
	}
	if res.Disposition != DispositionCreated || res.Created != 1 {
		t.Fatalf("res = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(worktrees, "feat", "repo-b", ".git")); err != nil {
		t.Fatal("repo-b worktree should have been created")
	}
}

// 全リポジトリが作成済みの対話選択は「追加するリポジトリはありません」で正常終了し、
// before は事前に・after は最後に実行される（before の実行は旧ショートサーキットで
// スキップされていた — 意図的な仕様変更）。selector は呼ばれない。
func TestInteractiveModeAllCreatedRunsBeforeAndAfterHooks(t *testing.T) {
	repos := t.TempDir()
	worktrees := t.TempDir()
	testutil.CloneWithBranchNamed(t, repos, "main", "repo-a")

	cfg := cfgOf(t, "feat", repos, worktrees)
	cfg.All = true
	if _, err := Run(t.Context(), deps(t, nil), cfg); err != nil {
		t.Fatal(err)
	}

	beforeMarker := filepath.Join(worktrees, "before-ran")
	afterMarker := filepath.Join(worktrees, "after-ran")
	cfg = cfgOf(t, "feat", repos, worktrees)
	cfg.Hooks = hooks.Config{
		Before: []hooks.Hook{{Name: "pre", Command: cmdspec.FromString("touch \"" + beforeMarker + "\"")}},
		After:  []hooks.Hook{{Name: "nav", Command: cmdspec.FromString("touch \"" + afterMarker + "\"")}},
	}
	res, err := Run(t.Context(), deps(t, func([]repo.Repo) ([]repo.Repo, error) {
		t.Fatal("selector must not run when every repository is already a member")
		return nil, nil
	}), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Disposition != DispositionNothingToAdd {
		t.Fatalf("disposition = %q, want %q", res.Disposition, DispositionNothingToAdd)
	}
	if len(res.Hooks) != 2 || res.Hooks[0].Timing != "before" || res.Hooks[1].Timing != "after" {
		t.Fatalf("hooks = %+v", res.Hooks)
	}
	for _, marker := range []string{beforeMarker, afterMarker} {
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("hook marker %s missing (before と after は両方実行される)", marker)
		}
	}
}

// 名前指定モード（--repo / MCP の repos）は、ルートが既存でも短絡しない。名前を
// 明示した呼び出しには「そのリポジトリに作成する」意図があるためである。
func TestNamedReposDoNotShortCircuitOnExistingRoot(t *testing.T) {
	repos := t.TempDir()
	testutil.CloneWithBranchNamed(t, repos, "main", "repo-a")
	worktrees := t.TempDir()
	if err := os.MkdirAll(filepath.Join(worktrees, "feat"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := cfgOf(t, "feat", repos, worktrees)
	cfg.Repos = []string{"repo-a"}

	// selector は名前指定モードでは使われない（nil で非対話 — MCP と同じ経路）。
	res, err := Run(t.Context(), deps(t, nil), cfg)
	if err != nil {
		t.Fatalf("err = %v\nres = %+v", err, res)
	}
	if res.Disposition != DispositionCreated || res.Created != 1 {
		t.Fatalf("named mode must not short-circuit: %+v", res)
	}
}

// 存在しないリポジトリ名の明示はエラーになる（意図的な仕様変更: 旧実装の
// RetainNamed は黙って取り除き、「何もすることがない」成功に化けていた）。
func TestUnknownNamedRepoIsError(t *testing.T) {
	repos := t.TempDir()
	testutil.CloneWithBranchNamed(t, repos, "main", "repo-a")
	cfg := cfgOf(t, "feat", repos, t.TempDir())
	cfg.Repos = []string{"repo-a", "does-not-exist"}

	_, err := Run(t.Context(), deps(t, nil), cfg)
	if err == nil {
		t.Fatal("unknown repo name should be an error")
	}
	if !strings.Contains(err.Error(), `"does-not-exist"`) || !strings.Contains(err.Error(), "見つかりません") {
		t.Fatalf("err = %q", err)
	}
	if !strings.Contains(err.Error(), repos) {
		t.Fatalf("エラーは repos_dir を含むべき: %q", err)
	}
}

// --all はプロンプトなしで探索された全リポジトリを対象にする。
func TestAllModeCreatesInEveryRepo(t *testing.T) {
	repos := t.TempDir()
	worktrees := t.TempDir()
	testutil.CloneWithBranchNamed(t, repos, "main", "repo-a")
	testutil.CloneWithBranchNamed(t, repos, "main", "repo-b")
	cfg := cfgOf(t, "feat", repos, worktrees)
	cfg.All = true

	res, err := Run(t.Context(), deps(t, nil), cfg)
	if err != nil {
		t.Fatalf("err = %v\nres = %+v", err, res)
	}
	if res.Created != 2 {
		t.Fatalf("res = %+v", res)
	}
}

// cfg.Base（--base / MCP の base パラメータ）を明示すると、その他ブランチではなく
// 明示されたブランチの最新コミットから worktree が作成される。
func TestBaseOverrideCreatesFromExplicitBranch(t *testing.T) {
	repos := t.TempDir()
	worktrees := t.TempDir()
	repoPath := testutil.CloneWithBranchNamed(t, repos, "main", "repo-a")

	// origin に develop ブランチを作り、main とは異なる内容をコミットする。
	testutil.Git(t, repoPath, "checkout", "-b", "develop")
	if err := os.WriteFile(filepath.Join(repoPath, "develop-only.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	testutil.Git(t, repoPath, "add", "-A")
	testutil.Git(t, repoPath, "commit", "-m", "develop commit")
	testutil.Git(t, repoPath, "push", "origin", "develop")
	testutil.Git(t, repoPath, "checkout", "main")

	cfg := cfgOf(t, "feat", repos, worktrees)
	cfg.Base = "develop"

	res, err := Run(t.Context(), deps(t, func(r []repo.Repo) ([]repo.Repo, error) { return r, nil }), cfg)
	if err != nil {
		t.Fatalf("err = %v\nres = %+v", err, res)
	}
	if _, err := os.Stat(filepath.Join(worktrees, "feat", "repo-a", "develop-only.txt")); err != nil {
		t.Fatal("worktree should be created from the develop branch (cfg.Base), not main")
	}
}

// 非 TTY（Selector が nil）で --repo / --all も未指定なら、フックや探索に触れる前に
// 使い方エラーになる。
func TestInteractiveModeWithoutSelectorIsError(t *testing.T) {
	repos := t.TempDir()
	testutil.CloneWithBranchNamed(t, repos, "main", "repo-a")
	cfg := cfgOf(t, "feat", repos, t.TempDir())
	// フックが実行されないことも同時に固定する（ガードは before フックより前）。
	marker := filepath.Join(t.TempDir(), "before-ran")
	cfg.Hooks = hooks.Config{Before: []hooks.Hook{{Name: "pre", Command: cmdspec.FromString("touch \"" + marker + "\"")}}}

	res, err := Run(t.Context(), deps(t, nil), cfg)
	if err == nil {
		t.Fatal("interactive mode without a selector should be an error")
	}
	if !strings.Contains(err.Error(), "--repo か --all を指定してください") {
		t.Fatalf("err = %q", err)
	}
	if res != nil {
		t.Fatalf("usage error should not produce a result: %+v", res)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("before hook must not run on a usage error")
	}
}

// before フックの失敗は、リポジトリに触れる前に中断させる。フックの結果は Result に
// 保持され、エラーが返る（Disposition = aborted）。
func TestBeforeHookFailureAborts(t *testing.T) {
	repos := t.TempDir()
	testutil.CloneWithBranchNamed(t, repos, "main", "repo-a")
	cfg := cfgOf(t, "feat", repos, t.TempDir())
	cfg.Hooks = hooks.Config{Before: []hooks.Hook{{Name: "guard", Command: cmdspec.FromString("false")}}}

	res, err := Run(t.Context(), deps(t, func(r []repo.Repo) ([]repo.Repo, error) {
		t.Fatal("selector must not run after a fatal before hook")
		return nil, nil
	}), cfg)
	if err == nil || !strings.Contains(err.Error(), "before フック") {
		t.Fatalf("err = %v", err)
	}
	if res == nil || res.Disposition != DispositionAborted {
		t.Fatalf("res = %+v", res)
	}
	if len(res.Hooks) != 1 || res.Hooks[0].Status != HookFailed {
		t.Fatalf("hooks = %+v", res.Hooks)
	}
}

// (d) コピーの部分失敗: worktree の作成は Created のまま、Copy レポートの
// Failures で区別される（Stage は "copy"）。
func TestCopyPartialFailureStaysCreatedWithReport(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root は権限エラーを起こせない")
	}
	repos := t.TempDir()
	repoPath := testutil.CloneWithBranchNamed(t, repos, "main", "repo-a")
	// コピー対象: 読めるファイルと読めないファイル。
	if err := os.WriteFile(filepath.Join(repoPath, ".env"), []byte("TOKEN=abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoPath, "secret.bin"), []byte("x"), 0o000); err != nil {
		t.Fatal(err)
	}
	worktrees := t.TempDir()
	cfg := cfgOf(t, "feat", repos, worktrees)
	cfg.Repos = []string{"repo-a"}
	cfg.RepoConfigs = map[string]config.RepoConfig{"repo-a": {Copy: config.CopySpec{Paths: []string{".env", "secret.bin"}}}}

	res, err := Run(t.Context(), deps(t, nil), cfg)
	if err != nil {
		t.Fatalf("コピーの部分失敗は create を失敗させない: %v", err)
	}
	if res.Created != 1 || res.Failed != 0 {
		t.Fatalf("res = %+v", res)
	}
	ro := res.Repos[0]
	if ro.Status != RepoCreated {
		t.Fatalf("status = %q, want created", ro.Status)
	}
	if ro.Copy == nil || len(ro.Copy.Failures) != 1 || ro.Copy.Failures[0].Path != "secret.bin" {
		t.Fatalf("copy = %+v", ro.Copy)
	}
	if len(ro.Copy.Copied) != 1 || ro.Copy.Copied[0] != ".env" {
		t.Fatalf("copied = %+v", ro.Copy.Copied)
	}
	if ro.Stage != "copy" {
		t.Fatalf("stage = %q, want copy", ro.Stage)
	}
	if got, err := os.ReadFile(filepath.Join(worktrees, "feat", "repo-a", ".env")); err != nil || string(got) != "TOKEN=abc\n" {
		t.Fatalf(".env not copied: %v %q", err, got)
	}
}

// post-create のコピーは明示パスと gitignored モードの両方を適用する
// （旧 worktree.Process 内のコピーから移設された挙動の回帰テスト）。
func TestCopyExtrasGitignoredMode(t *testing.T) {
	repos := t.TempDir()
	repoPath := testutil.CloneWithBranchNamed(t, repos, "main", "repo-a")
	if err := os.WriteFile(filepath.Join(repoPath, ".gitignore"), []byte("ignored.txt\nnode_modules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoPath, "ignored.txt"), []byte("SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repoPath, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoPath, "node_modules", "dep.js"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	worktrees := t.TempDir()
	cfg := cfgOf(t, "feat", repos, worktrees)
	cfg.Repos = []string{"repo-a"}
	cfg.RepoConfigs = map[string]config.RepoConfig{"repo-a": {Copy: config.CopySpec{Gitignored: true, Exclude: []string{"node_modules"}}}}

	res, err := Run(t.Context(), deps(t, nil), cfg)
	if err != nil {
		t.Fatalf("err = %v\nres = %+v", err, res)
	}
	target := filepath.Join(worktrees, "feat", "repo-a")
	if data, _ := os.ReadFile(filepath.Join(target, "ignored.txt")); string(data) != "SECRET" {
		t.Fatalf("ignored.txt not copied: %q", data)
	}
	if _, err := os.Stat(filepath.Join(target, "node_modules")); err == nil {
		t.Fatal("excluded node_modules must not be copied")
	}
	if ro := res.Repos[0]; ro.Copy == nil || len(ro.Copy.Copied) == 0 {
		t.Fatalf("copy report missing: %+v", ro.Copy)
	}
}

// create が同名の worktree ルートを削除して新規作成し直したとき、古い setup 記録
// （invalidateSetupRecords 経路）が無効化されることを確認する回帰テスト。record.Path
// の実在チェックによる二重防御があっても、記録自体を消す経路が壊れていないことを
// 独立に固定する。
func TestCreateInvalidatesStaleSetupRecordsForRecreatedWorktree(t *testing.T) {
	repos := t.TempDir()
	worktrees := t.TempDir()
	testutil.CloneWithBranchNamed(t, repos, "main", "repo-a")

	stateRoot := statedir.At(t.TempDir())
	stateStore := coreserver.NewStateStore(stateRoot)
	if err := stateStore.Update(t.Context(), func(s *coreserver.State) (bool, error) {
		s.Repo("repo-a").Server("backend").RecordSetup("feat", "/stale/path")
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}

	cfg := cfgOf(t, "feat", repos, worktrees)
	cfg.Repos = []string{"repo-a"}

	res, err := Run(t.Context(), depsAt(stateRoot, nil), cfg)
	if err != nil {
		t.Fatalf("err = %v\nres = %+v", err, res)
	}
	if res.SetupInvalidateError != "" {
		t.Fatalf("setup invalidate warning: %q", res.SetupInvalidateError)
	}

	if err := stateStore.View(t.Context(), func(s *coreserver.State) error {
		rs := s.Repos["repo-a"]
		if rs == nil || rs.Servers["backend"] == nil {
			return nil
		}
		if _, ok := rs.Servers["backend"].Setup["feat"]; ok {
			t.Fatal("stale setup record should have been invalidated by create")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
