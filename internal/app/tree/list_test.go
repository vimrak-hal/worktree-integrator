package tree

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
	coreserver "github.com/vimrak-hal/worktree-integrator/internal/core/server"
	"github.com/vimrak-hal/worktree-integrator/internal/core/server/serverfake"
	"github.com/vimrak-hal/worktree-integrator/internal/core/wtenv"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/testutil"
)

// List は inventory・別名・稼働サーバーを 1 つのビューに統合する。壊れた
// チェックアウトは Broken として現れる。
func TestListIntegratesAliasAndServers(t *testing.T) {
	reposDir := t.TempDir()
	worktreesDir := t.TempDir()
	repoA := testutil.CloneWithBranchNamed(t, reposDir, "main", "repo-a")
	addWorktree(t, repoA, "feat-x", filepath.Join(worktreesDir, "feat-x", "repo-a"))
	addWorktree(t, repoA, "old-x", filepath.Join(worktreesDir, "old-x", "repo-a"))

	fake := serverfake.New()
	d := newDeps(t, fake, &config.File{}, reposDir, worktreesDir)

	// feat-x に別名を付ける。
	if _, err := d.Aliases.Set(t.Context(), "feat-x", "ログイン画面の修正"); err != nil {
		t.Fatal(err)
	}

	// feat-x でサーバーが稼働している状態を作る（fake が生存を報告する Ident）。
	ident, err := fake.SpawnDetached("run", "", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Store.Update(t.Context(), func(s *coreserver.State) (bool, error) {
		s.Repo("repo-a").Servers["backend"] = &coreserver.Runtime{
			Running: &coreserver.Instance{Ident: ident, Worktree: "feat-x", Log: "/tmp/x.log"},
		}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}

	// old-x のチェックアウトだけを壊す: gitdir ポインタを実在しない先へ向ける
	// （ソースリポジトリを作り直した rm -rf 残骸の再現）。
	if err := os.WriteFile(filepath.Join(worktreesDir, "old-x", "repo-a", ".git"),
		[]byte("gitdir: /nonexistent/location\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := List(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Worktrees) != 2 {
		t.Fatalf("worktrees = %+v", res.Worktrees)
	}
	featX := res.Worktrees[0]
	if featX.Name != "feat-x" || featX.Alias != "ログイン画面の修正" {
		t.Fatalf("featX = %+v", featX)
	}
	if len(featX.Servers) != 1 || featX.Servers[0].Repo != "repo-a" ||
		featX.Servers[0].Server != "backend" || featX.Servers[0].Pid != ident.Pid {
		t.Fatalf("featX servers = %+v", featX.Servers)
	}
	oldX := res.Worktrees[1]
	if oldX.Name != "old-x" || !oldX.Broken {
		t.Fatalf("oldX = %+v", oldX)
	}
	if len(oldX.Repos) != 1 || oldX.Repos[0].Healthy {
		t.Fatalf("oldX repos = %+v", oldX.Repos)
	}
}

// 消滅済みプロセスの稼働記録は List の中で自己修復され（Probe）、SERVERS には
// 現れない。
func TestListSelfHealsDeadRunning(t *testing.T) {
	reposDir := t.TempDir()
	worktreesDir := t.TempDir()
	repoA := testutil.CloneWithBranchNamed(t, reposDir, "main", "repo-a")
	addWorktree(t, repoA, "feat-x", filepath.Join(worktreesDir, "feat-x", "repo-a"))

	fake := serverfake.New()
	d := newDeps(t, fake, &config.File{}, reposDir, worktreesDir)

	// fake が関知しない Ident（= 消滅済み）を稼働記録として書く。
	if err := d.Store.Update(t.Context(), func(s *coreserver.State) (bool, error) {
		s.Repo("repo-a").Servers["backend"] = &coreserver.Runtime{
			Running: &coreserver.Instance{Worktree: "feat-x"},
		}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}

	res, err := List(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Worktrees) != 1 || len(res.Worktrees[0].Servers) != 0 {
		t.Fatalf("res = %+v", res.Worktrees)
	}
	// 記録は自己修復（クリア）されて永続化されている。
	if err := d.Store.View(t.Context(), func(s *coreserver.State) error {
		if rt := s.Repos["repo-a"].Servers["backend"]; rt.Running != nil {
			t.Fatalf("dead running record should be healed: %+v", rt.Running)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// worktree が 1 つも無ければ空の一覧（Worktrees は非 nil）。
func TestListEmpty(t *testing.T) {
	reposDir := t.TempDir()
	d := newDeps(t, serverfake.New(), &config.File{}, reposDir, filepath.Join(t.TempDir(), "none"))
	res, err := List(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}
	if res.Worktrees == nil || len(res.Worktrees) != 0 {
		t.Fatalf("res = %#v", res)
	}
}

// list は worktree の列挙に repos_dir を必要としない。repos_dir が存在しなくても
// 成功する（worktree の実体スキャンはソースリポジトリ側の探索に依存しない）。
func TestListSucceedsWithoutReposDir(t *testing.T) {
	srcDir := t.TempDir()
	worktreesDir := t.TempDir()
	repoA := testutil.CloneWithBranchNamed(t, srcDir, "main", "repo-a")
	addWorktree(t, repoA, "feat-x", filepath.Join(worktreesDir, "feat-x", "repo-a"))

	// repos_dir は実在しないパスを指す。
	reposDir := filepath.Join(t.TempDir(), "does-not-exist")
	d := newDeps(t, serverfake.New(), &config.File{}, reposDir, worktreesDir)

	res, err := List(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Worktrees) != 1 || res.Worktrees[0].Name != "feat-x" {
		t.Fatalf("worktrees = %+v", res.Worktrees)
	}
}

// wtenv の Root 導出（<worktrees_dir>/<name>）と List の Root 表示が一致する
// （表示と実体のパスがずれないことの固定）。
func TestListRootMatchesRunContext(t *testing.T) {
	reposDir := t.TempDir()
	worktreesDir := t.TempDir()
	repoA := testutil.CloneWithBranchNamed(t, reposDir, "main", "repo-a")
	addWorktree(t, repoA, "feat-x", filepath.Join(worktreesDir, "feat-x", "repo-a"))

	d := newDeps(t, serverfake.New(), &config.File{}, reposDir, worktreesDir)
	res, err := List(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}
	want := wtenv.NewRunContext("feat-x", reposDir, worktreesDir).Root
	if res.Worktrees[0].Root != want {
		t.Fatalf("root = %q, want %q", res.Worktrees[0].Root, want)
	}
}
