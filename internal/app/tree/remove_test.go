package tree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vimrak-hal/worktree-integrator/internal/app/action"
	"github.com/vimrak-hal/worktree-integrator/internal/app/action/actiontest"
	"github.com/vimrak-hal/worktree-integrator/internal/core/cmdspec"
	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
	"github.com/vimrak-hal/worktree-integrator/internal/core/git"
	coreserver "github.com/vimrak-hal/worktree-integrator/internal/core/server"
	"github.com/vimrak-hal/worktree-integrator/internal/core/server/serverfake"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/testutil"
)

// serverConfig は repo-a に backend サーバーを 1 つ持つ設定を返す。
func serverConfig() *config.File {
	return &config.File{Repos: map[string]config.RepoConfig{
		"repo-a": {Servers: map[string]coreserver.Spec{
			"backend": {Start: cmdspec.FromString("run-server")},
		}},
	}}
}

// removeFixture は repo-a と feat-x worktree（チェックアウト 1 つ）を用意する。
func removeFixture(t *testing.T, proc coreserver.ProcessControl, cfg *config.File) (d Deps, repoA, root string) {
	t.Helper()
	reposDir := t.TempDir()
	worktreesDir := t.TempDir()
	repoA = testutil.CloneWithBranchNamed(t, reposDir, "main", "repo-a")
	root = filepath.Join(worktreesDir, "feat-x")
	addWorktree(t, repoA, "feat-x", filepath.Join(root, "repo-a"))
	return newDeps(t, proc, cfg, reposDir, worktreesDir), repoA, root
}

// クリーンな worktree の削除: チェックアウト・ブランチ・setup 記録・別名・ログ・
// ルートのすべてが片付く。
func TestRemoveCleansEverything(t *testing.T) {
	fake := serverfake.New()
	d, repoA, root := removeFixture(t, fake, serverConfig())

	// 稼働中サーバー・setup 記録・別名・ログ（.prev 含む）を用意する。
	ident, err := fake.SpawnDetached("run-server", "", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	logPath := d.Store.LogPath("repo-a", "backend", "feat-x")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{logPath, coreserver.PrevLogPath(logPath)} {
		if err := os.WriteFile(p, []byte("log\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// 無関係な worktree のログは残ること。
	otherLog := d.Store.LogPath("repo-a", "backend", "other-wt")
	if err := os.WriteFile(otherLog, []byte("log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := d.Store.Update(t.Context(), func(s *coreserver.State) (bool, error) {
		rt := s.Repo("repo-a").Server("backend")
		rt.Running = &coreserver.Instance{Ident: ident, Worktree: "feat-x", Log: logPath}
		rt.RecordSetup("feat-x", filepath.Join(root, "repo-a"))
		rt.RecordSetup("other-wt", "/somewhere/else")
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Aliases.Set(t.Context(), "feat-x", "Login"); err != nil {
		t.Fatal(err)
	}

	res, err := Remove(t.Context(), d, action.Remove{Name: actiontest.MustName(t, "feat-x")})
	if err != nil {
		t.Fatalf("err = %v\nres = %+v", err, res)
	}

	// サーバーは停止された（同一性検証を通ってシグナルが送られた）。
	if res.Stop == nil || res.Stop.Stopped != 1 {
		t.Fatalf("stop = %+v", res.Stop)
	}
	if signals := fake.StopSignals(); len(signals) != 1 || signals[0] != ident.Pgid {
		t.Fatalf("stop signals = %v", signals)
	}
	// チェックアウトとブランチが消えた。
	if len(res.Repos) != 1 || !res.Repos[0].Removed || !res.Repos[0].BranchDeleted {
		t.Fatalf("repos = %+v", res.Repos)
	}
	if exists, err := git.LocalBranchExists(t.Context(), repoA, "feat-x"); err != nil || exists {
		t.Fatalf("branch should be deleted: exists=%v err=%v", exists, err)
	}
	// setup 記録は feat-x の分だけ消え、他の worktree の記録は残る。
	if res.SetupCleared != 1 {
		t.Fatalf("setup cleared = %d", res.SetupCleared)
	}
	if err := d.Store.View(t.Context(), func(s *coreserver.State) error {
		rt := s.Repos["repo-a"].Servers["backend"]
		if _, ok := rt.Setup["feat-x"]; ok {
			t.Fatal("feat-x setup record should be gone")
		}
		if _, ok := rt.Setup["other-wt"]; !ok {
			t.Fatal("other-wt setup record must survive")
		}
		if rt.Running != nil {
			t.Fatal("running record should be cleared by the stop")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// 別名・ログ（.prev 含む）・ルートが消えた。無関係なログは残る。
	if !res.AliasRemoved {
		t.Fatal("alias should be removed")
	}
	if len(res.LogsRemoved) != 2 {
		t.Fatalf("logs removed = %v", res.LogsRemoved)
	}
	if _, err := os.Stat(otherLog); err != nil {
		t.Fatal("unrelated log must survive")
	}
	if !res.RootRemoved {
		t.Fatal("root should be removed")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("root should be gone: %v", err)
	}
}

// dirty なチェックアウトは git が拒否し、エラーが表面化する。ルートは残る
// （進められるところまで進めるが、残ったチェックアウトを巻き込まない）。
func TestRemoveRefusesDirtyWorktree(t *testing.T) {
	d, _, root := removeFixture(t, serverfake.New(), &config.File{})
	dirty := filepath.Join(root, "repo-a", "dirty.txt")
	if err := os.WriteFile(dirty, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Remove(t.Context(), d, action.Remove{Name: actiontest.MustName(t, "feat-x")})
	if err == nil {
		t.Fatal("dirty removal should be an error")
	}
	if len(res.Repos) != 1 || res.Repos[0].Removed || res.Repos[0].Error == "" {
		t.Fatalf("repos = %+v", res.Repos)
	}
	if res.RootRemoved {
		t.Fatal("root must not be removed when a checkout survives")
	}
	if _, err := os.Stat(dirty); err != nil {
		t.Fatal("dirty checkout must be left in place")
	}
}

// --force は dirty の拒否を上書きする。
func TestRemoveForceOverridesDirty(t *testing.T) {
	d, repoA, root := removeFixture(t, serverfake.New(), &config.File{})
	if err := os.WriteFile(filepath.Join(root, "repo-a", "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Remove(t.Context(), d, action.Remove{Name: actiontest.MustName(t, "feat-x"), Force: true})
	if err != nil {
		t.Fatalf("err = %v\nres = %+v", err, res)
	}
	if !res.RootRemoved {
		t.Fatalf("res = %+v", res)
	}
	if exists, _ := git.LocalBranchExists(t.Context(), repoA, "feat-x"); exists {
		t.Fatal("branch should be deleted with the worktree")
	}
}

// --keep-branch はブランチを残す。
func TestRemoveKeepBranch(t *testing.T) {
	d, repoA, _ := removeFixture(t, serverfake.New(), &config.File{})

	res, err := Remove(t.Context(), d, action.Remove{Name: actiontest.MustName(t, "feat-x"), KeepBranch: true})
	if err != nil {
		t.Fatalf("err = %v\nres = %+v", err, res)
	}
	if len(res.Repos) != 1 || !res.Repos[0].Removed || res.Repos[0].BranchDeleted {
		t.Fatalf("repos = %+v", res.Repos)
	}
	if exists, err := git.LocalBranchExists(t.Context(), repoA, "feat-x"); err != nil || !exists {
		t.Fatalf("branch should survive with --keep-branch: exists=%v err=%v", exists, err)
	}
}

// サーバーの停止に失敗した場合は削除を中断する: チェックアウト・別名・ログには
// 一切触れない。
func TestRemoveAbortsWhenStopFails(t *testing.T) {
	fake := serverfake.New()
	fake.StopError = errors.New("still alive")
	d, _, root := removeFixture(t, fake, serverConfig())

	ident, err := fake.SpawnDetached("run-server", "", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Store.Update(t.Context(), func(s *coreserver.State) (bool, error) {
		s.Repo("repo-a").Server("backend").Running = &coreserver.Instance{Ident: ident, Worktree: "feat-x"}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Aliases.Set(t.Context(), "feat-x", "Login"); err != nil {
		t.Fatal(err)
	}

	res, err := Remove(t.Context(), d, action.Remove{Name: actiontest.MustName(t, "feat-x")})
	if err == nil || !strings.Contains(err.Error(), "削除を中断") {
		t.Fatalf("err = %v", err)
	}
	if len(res.Repos) != 0 || res.RootRemoved {
		t.Fatalf("res = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(root, "repo-a", ".git")); err != nil {
		t.Fatal("checkout must be untouched after an aborted remove")
	}
	// 別名も残る（中断はステップ 1 で起きる）。
	if v, ok, _ := d.Aliases.Get(t.Context(), "feat-x"); !ok || v != "Login" {
		t.Fatalf("alias should survive: %q %v", v, ok)
	}
	// Running の記録も保持される（孤児を台帳から消さない）。
	if err := d.Store.View(t.Context(), func(s *coreserver.State) error {
		if s.Repos["repo-a"].Servers["backend"].Running == nil {
			t.Fatal("running record must be kept after a failed stop")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// ネストした名前（feature/login）の削除は、空になった中間ディレクトリ（feature/）も
// 片付ける（list に空の worktree として残さない）。
func TestRemoveNestedNameCleansEmptyParents(t *testing.T) {
	reposDir := t.TempDir()
	worktreesDir := t.TempDir()
	repoA := testutil.CloneWithBranchNamed(t, reposDir, "main", "repo-a")
	addWorktree(t, repoA, "feature/login", filepath.Join(worktreesDir, "feature", "login", "repo-a"))
	// 兄弟 worktree があれば親は残ること。
	addWorktree(t, repoA, "feature/signup", filepath.Join(worktreesDir, "feature", "signup", "repo-a"))
	d := newDeps(t, serverfake.New(), &config.File{}, reposDir, worktreesDir)

	res, err := Remove(t.Context(), d, action.Remove{Name: actiontest.MustName(t, "feature/login")})
	if err != nil {
		t.Fatalf("err = %v\nres = %+v", err, res)
	}
	if !res.RootRemoved {
		t.Fatalf("res = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(worktreesDir, "feature", "signup")); err != nil {
		t.Fatal("sibling worktree must survive")
	}

	if res, err := Remove(t.Context(), d, action.Remove{Name: actiontest.MustName(t, "feature/signup")}); err != nil || !res.RootRemoved {
		t.Fatalf("second remove: err=%v res=%+v", err, res)
	}
	// 兄弟も消えたので、空になった feature/ ごと片付く。
	if _, err := os.Stat(filepath.Join(worktreesDir, "feature")); !os.IsNotExist(err) {
		t.Fatalf("empty parent directory should be cleaned up: %v", err)
	}
	if _, err := os.Stat(worktreesDir); err != nil {
		t.Fatal("worktrees_dir itself must survive")
	}
}

// removeError は各ステップの元エラーを型ごと保つ（errors.Join で連結する）。member
// 削除がキャンセルで中断しても返るエラーは errors.Is(context.Canceled) を満たし、
// exit 130 は main の ctx.Err() フォールバックに頼らず自然に成立する（旧実装は DTO の
// 文字列を errors.New で再構成しており、型・チェーンが失われていた）。
func TestRemoveErrorPreservesErrorType(t *testing.T) {
	err := removeError("feat-x", []error{fmt.Errorf("repo-a: %w", context.Canceled)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("removeError should preserve context.Canceled, got %v", err)
	}
	if !strings.Contains(err.Error(), `worktree "feat-x" の削除が完了しませんでした`) {
		t.Fatalf("message = %q", err.Error())
	}
	if removeError("feat-x", nil) != nil {
		t.Fatal("failure が無ければ nil を返すべき")
	}
}

// 存在しない worktree の削除はエラー。
func TestRemoveMissingWorktreeIsError(t *testing.T) {
	d := newDeps(t, serverfake.New(), &config.File{}, t.TempDir(), t.TempDir())
	_, err := Remove(t.Context(), d, action.Remove{Name: actiontest.MustName(t, "no-such")})
	if err == nil || !strings.Contains(err.Error(), `worktree "no-such" がありません`) {
		t.Fatalf("err = %v", err)
	}
}

// 壊れたチェックアウト（gitdir ポインタ死亡）は git を介さず直接削除され、ソース側の
// 残骸メタデータは prune される。
func TestRemoveBrokenCheckout(t *testing.T) {
	d, repoA, root := removeFixture(t, serverfake.New(), &config.File{})
	// gitdir ポインタを壊す（ソースを作り直した状況の再現）。
	if err := os.WriteFile(filepath.Join(root, "repo-a", ".git"),
		[]byte("gitdir: /nonexistent/location\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Remove(t.Context(), d, action.Remove{Name: actiontest.MustName(t, "feat-x")})
	if err != nil {
		t.Fatalf("err = %v\nres = %+v", err, res)
	}
	if len(res.Repos) != 1 || !res.Repos[0].Removed || !res.RootRemoved {
		t.Fatalf("res = %+v", res)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("broken checkout should be removed directly")
	}
	// ソース側の worktree メタデータは prune 済み（dry-run が空）。
	if out, err := git.PruneWorktreesDryRun(t.Context(), repoA); err != nil || strings.TrimSpace(out) != "" {
		t.Fatalf("source metadata should be pruned: %q, %v", out, err)
	}
}
