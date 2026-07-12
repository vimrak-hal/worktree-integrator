package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vimrak-hal/worktree-integrator/internal/app/action"
	"github.com/vimrak-hal/worktree-integrator/internal/core/cmdspec"
	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
	"github.com/vimrak-hal/worktree-integrator/internal/core/git"
	coreserver "github.com/vimrak-hal/worktree-integrator/internal/core/server"
	"github.com/vimrak-hal/worktree-integrator/internal/core/server/serverfake"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/childio"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/statedir"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/testutil"
)

func noEnv(string) string { return "" }

// lifecycleApp は実ローカル git リポジトリ（repo-a）+ serverfake で App を構築する。
// tree 系メソッド（Remove / Doctor / List / Enter）はディレクトリを設定と環境変数から
// 解決するため、WT_* を消して Config の値だけが効くようにする。
func lifecycleApp(t *testing.T) (a *App, fake *serverfake.Fake, repoA string) {
	t.Helper()
	reposDir := t.TempDir()
	worktreesDir := t.TempDir()
	repoA = testutil.CloneWithBranchNamed(t, reposDir, "main", "repo-a")
	t.Setenv("WT_REPOS_DIR", "")
	t.Setenv("WT_WORKTREES_DIR", "")

	cfg := &config.File{
		ReposDir:     reposDir,
		WorktreesDir: worktreesDir,
		Repos: map[string]config.RepoConfig{
			"repo-a": {Servers: map[string]coreserver.Spec{
				"backend": {
					Start: cmdspec.FromString("run-server"),
					Setup: ptrCommands("setup-once"),
				},
			}},
		},
	}
	fake = serverfake.New()
	a = New(cfg, statedir.At(t.TempDir()), childio.Streams{})
	// Proc はプロセスを起動しないフェイクへ差し替える（本番の UnixProcess は使わない）。
	a.Proc = fake
	return a, fake, repoA
}

func ptrCommands(s string) *cmdspec.Commands {
	c := cmdspec.FromString(s)
	return &c
}

// createFeatX は repo-a に feat-x worktree を作成する。
func createFeatX(t *testing.T, a *App) {
	t.Helper()
	act, err := action.NewCreate("feat-x", []string{"repo-a"}, false, "", action.Overrides{}, a.Config, noEnv, os.UserHomeDir)
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Create(t.Context(), act)
	if err != nil || res.Created != 1 {
		t.Fatalf("create: err=%v res=%+v", err, res)
	}
}

// switchFeatX は feat-x へサーバーを切り替える。
func switchFeatX(t *testing.T, a *App) {
	t.Helper()
	cmd, err := action.NewServerCommand(action.Overrides{}, a.Config, noEnv, os.UserHomeDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	name, err := action.ParseName("feat-x")
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.ServerSwitch(t.Context(), cmd, action.SwitchKind{Name: name})
	if err != nil || res.Started != 1 {
		t.Fatalf("switch: err=%v res=%+v", err, res)
	}
}

// setupRuns は fake が実行した setup コマンドの回数を返す。
func setupRuns(fake *serverfake.Fake) int {
	n := 0
	for _, script := range fake.ForegroundRuns() {
		if strings.Contains(script, "setup-once") {
			n++
		}
	}
	return n
}

// E2E (a): create → switch（setup 実行）→ remove → 同名で再 create → 再 switch で
// setup が再実行される。remove は稼働中サーバーの停止・チェックアウト・ブランチ・
// setup 記録・別名・ログ・ルートのすべてを片付ける。
func TestRemoveThenRecreateRerunsSetup(t *testing.T) {
	a, fake, repoA := lifecycleApp(t)
	worktreesDir := a.Config.WorktreesDir

	createFeatX(t, a)
	switchFeatX(t, a)
	if setupRuns(fake) != 1 {
		t.Fatalf("setup should have run once, got %d", setupRuns(fake))
	}

	// 別名と、切り替えが記録したログパスに実ファイルを置く（fake はログを書かない）。
	name, _ := action.ParseName("feat-x")
	if _, err := a.AliasSet(t.Context(), name, "Login"); err != nil {
		t.Fatal(err)
	}
	logPath := coreserver.NewStateStore(a.Root).LogPath("repo-a", "backend", "feat-x")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("log\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := a.Remove(t.Context(), action.Remove{Name: name})
	if err != nil {
		t.Fatalf("remove: err=%v res=%+v", err, res)
	}
	if res.Stop == nil || res.Stop.Stopped != 1 {
		t.Fatalf("running server should be stopped: %+v", res.Stop)
	}
	if !res.RootRemoved || !res.AliasRemoved || res.SetupCleared != 1 || len(res.LogsRemoved) != 1 {
		t.Fatalf("remove result = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(worktreesDir, "feat-x")); !os.IsNotExist(err) {
		t.Fatalf("worktree root should be gone: %v", err)
	}
	// ブランチも消えているので、同名の再作成が成功する。
	createFeatX(t, a)
	switchFeatX(t, a)
	if setupRuns(fake) != 2 {
		t.Fatalf("setup should run again after remove + recreate, got %d runs", setupRuns(fake))
	}
	// 削除された repoA のメタデータ整合（prune 済みで再作成できたこと自体が証左だが、
	// 念のため実体も確認する）。
	if _, err := os.Stat(filepath.Join(worktreesDir, "feat-x", "repo-a", ".git")); err != nil {
		t.Fatalf("recreated checkout missing: %v (repo=%s)", err, repoA)
	}
}

// E2E (b): remove を経ない手動 rm -rf の後、doctor --fix が自己修復する —
// 消滅済みの稼働記録・setup 記録・別名・孤児ログが掃除され、git 側の残骸
// メタデータが prune される。
func TestManualRmRfThenDoctorFixSelfHeals(t *testing.T) {
	a, _, repoA := lifecycleApp(t)
	worktreesDir := a.Config.WorktreesDir

	createFeatX(t, a)
	switchFeatX(t, a)
	name, _ := action.ParseName("feat-x")
	if _, err := a.AliasSet(t.Context(), name, "Login"); err != nil {
		t.Fatal(err)
	}

	// 手動 rm -rf（remove を経ない）。
	if err := os.RemoveAll(filepath.Join(worktreesDir, "feat-x")); err != nil {
		t.Fatal(err)
	}
	// サーバーはその後クラッシュした（記録だけが残った）状況を作る: fake が関知
	// しない Ident に差し替える。LastLog の参照も消し、ログが孤児になるようにする。
	store := coreserver.NewStateStore(a.Root)
	logPath := store.LogPath("repo-a", "backend", "feat-x")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(t.Context(), func(s *coreserver.State) (bool, error) {
		rt := s.Repo("repo-a").Server("backend")
		rt.Running = &coreserver.Instance{Worktree: "feat-x", Log: logPath}
		rt.LastLog = ""
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}

	// 報告のみの実行は何も変更しない。
	report, err := a.Doctor(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Fixed != 0 {
		t.Fatalf("doctor without --fix must not fix: %+v", report)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatal("doctor without --fix must not delete logs")
	}

	res, err := a.Doctor(t.Context(), true)
	if err != nil {
		t.Fatalf("doctor --fix: %v", err)
	}
	fixedChecks := map[string]bool{}
	for _, f := range res.Findings {
		if f.Fixed {
			fixedChecks[f.Check] = true
		}
	}
	for _, check := range []string{"dead_running", "stale_setup", "stale_alias", "orphan_log", "prunable_worktrees"} {
		if !fixedChecks[check] {
			t.Errorf("check %s should have produced a fixed finding: %+v", check, res.Findings)
		}
	}

	// 状態・別名・ログ・git メタデータのすべてが掃除された。
	if err := store.View(t.Context(), func(s *coreserver.State) error {
		rt := s.Repos["repo-a"].Servers["backend"]
		if rt.Running != nil {
			t.Error("dead running record should be cleared")
		}
		if _, ok := rt.Setup["feat-x"]; ok {
			t.Error("stale setup record should be cleared")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if res2, err := a.AliasList(t.Context()); err != nil || len(res2.Aliases) != 0 {
		t.Fatalf("alias should be cleaned: %+v, %v", res2, err)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("orphan log should be deleted: %v", err)
	}
	// git 側の残骸メタデータも prune 済み（dry-run が空）。
	if out, err := git.PruneWorktreesDryRun(t.Context(), repoA); err != nil || strings.TrimSpace(out) != "" {
		t.Fatalf("worktree metadata should be pruned: %q, %v", out, err)
	}
	// 再実行では修復すべき発見が残っていない（rm -rf が残したローカルブランチは
	// doctor の対象外 — その完全な後始末は remove コマンドの責務である）。
	res, err = a.Doctor(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range res.Findings {
		if f.Fixable {
			t.Errorf("unexpected residual fixable finding: %+v", f)
		}
	}
}
