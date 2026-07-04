package tree

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vimrak-hal/worktree-integrator/internal/core/cmdspec"
	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
	coreserver "github.com/vimrak-hal/worktree-integrator/internal/core/server"
	"github.com/vimrak-hal/worktree-integrator/internal/core/server/serverfake"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/testutil"
)

// findingsOf は check 名でフィルタした発見を返す。
func findingsOf(res *DoctorResult, check string) []Finding {
	var out []Finding
	for _, f := range res.Findings {
		if f.Check == check {
			out = append(out, f)
		}
	}
	return out
}

// チェック 1: 消滅済みプロセスの稼働記録は報告され、--fix でクリアされる。
// 生きている稼働記録は報告されない。
func TestDoctorDeadRunning(t *testing.T) {
	fake := serverfake.New()
	reposDir := t.TempDir()
	worktreesDir := t.TempDir()
	repoA := testutil.CloneWithBranchNamed(t, reposDir, "main", "repo-a")
	addWorktree(t, repoA, "feat-x", filepath.Join(worktreesDir, "feat-x", "repo-a"))
	d := newDeps(t, fake, &config.File{}, reposDir, worktreesDir)

	alive, err := fake.SpawnDetached("run", "", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Store.Update(t.Context(), func(s *coreserver.State) (bool, error) {
		s.Repo("repo-a").Servers["backend"] = &coreserver.Runtime{
			Running: &coreserver.Instance{Ident: alive, Worktree: "feat-x"},
		}
		// fake が関知しない Ident = 消滅済み。
		s.Repo("repo-a").Servers["frontend"] = &coreserver.Runtime{
			Running: &coreserver.Instance{Worktree: "feat-x"},
		}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}

	// 報告のみ（--fix なし）: 記録は変更されない。
	res, err := Doctor(t.Context(), d, false)
	if err != nil {
		t.Fatal(err)
	}
	dead := findingsOf(res, "dead_running")
	if len(dead) != 1 || dead[0].Server != "frontend" || !dead[0].Fixable || dead[0].Fixed {
		t.Fatalf("dead findings = %+v", dead)
	}
	if err := d.Store.View(t.Context(), func(s *coreserver.State) error {
		if s.Repos["repo-a"].Servers["frontend"].Running == nil {
			t.Fatal("doctor without --fix must not change state")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// --fix: 消滅済みだけがクリアされ、生きている記録は残る。
	res, err = Doctor(t.Context(), d, true)
	if err != nil {
		t.Fatal(err)
	}
	dead = findingsOf(res, "dead_running")
	if len(dead) != 1 || !dead[0].Fixed || res.Fixed == 0 {
		t.Fatalf("fixed findings = %+v (res=%+v)", dead, res)
	}
	if err := d.Store.View(t.Context(), func(s *coreserver.State) error {
		if s.Repos["repo-a"].Servers["frontend"].Running != nil {
			t.Fatal("dead running record should be cleared by --fix")
		}
		if s.Repos["repo-a"].Servers["backend"].Running == nil {
			t.Fatal("alive running record must survive")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// チェック 2・3: 実在しない worktree の setup 記録と別名は報告され、--fix で消える。
// 実在するものは触られない。
func TestDoctorStaleSetupAndAlias(t *testing.T) {
	reposDir := t.TempDir()
	worktreesDir := t.TempDir()
	repoA := testutil.CloneWithBranchNamed(t, reposDir, "main", "repo-a")
	addWorktree(t, repoA, "feat-x", filepath.Join(worktreesDir, "feat-x", "repo-a"))
	d := newDeps(t, serverfake.New(), &config.File{}, reposDir, worktreesDir)

	if err := d.Store.Update(t.Context(), func(s *coreserver.State) (bool, error) {
		rt := s.Repo("repo-a").Server("backend")
		rt.RecordSetup("feat-x", filepath.Join(worktreesDir, "feat-x", "repo-a"))
		rt.RecordSetup("gone-wt", "/somewhere")
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	for name, label := range map[string]string{"feat-x": "Live", "gone-wt": "Stale"} {
		if _, err := d.Aliases.Set(t.Context(), name, label); err != nil {
			t.Fatal(err)
		}
	}

	res, err := Doctor(t.Context(), d, true)
	if err != nil {
		t.Fatal(err)
	}
	if stale := findingsOf(res, "stale_setup"); len(stale) != 1 || stale[0].Worktree != "gone-wt" || !stale[0].Fixed {
		t.Fatalf("stale_setup = %+v", stale)
	}
	if stale := findingsOf(res, "stale_alias"); len(stale) != 1 || stale[0].Worktree != "gone-wt" || !stale[0].Fixed {
		t.Fatalf("stale_alias = %+v", stale)
	}
	if err := d.Store.View(t.Context(), func(s *coreserver.State) error {
		setup := s.Repos["repo-a"].Servers["backend"].Setup
		if _, ok := setup["gone-wt"]; ok {
			t.Fatal("stale setup record should be removed")
		}
		if _, ok := setup["feat-x"]; !ok {
			t.Fatal("live setup record must survive")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	doc, err := d.Aliases.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := doc.Aliases["gone-wt"]; ok {
		t.Fatal("stale alias should be removed")
	}
	if doc.Aliases["feat-x"] != "Live" {
		t.Fatal("live alias must survive")
	}
}

// チェック 4: 孤児ログの判定 — state に参照されているログと実在 worktree のログは
// 残り、どちらでもないログ（.prev 含む）だけが --fix で削除される。命名規則に
// 従わないファイルには触れない。
func TestDoctorOrphanLogs(t *testing.T) {
	reposDir := t.TempDir()
	worktreesDir := t.TempDir()
	repoA := testutil.CloneWithBranchNamed(t, reposDir, "main", "repo-a")
	addWorktree(t, repoA, "feat-x", filepath.Join(worktreesDir, "feat-x", "repo-a"))
	d := newDeps(t, serverfake.New(), &config.File{}, reposDir, worktreesDir)

	logsDir := d.Root.LogsDir()
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path string) {
		t.Helper()
		if err := os.WriteFile(path, []byte("log\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	liveLog := d.Store.LogPath("repo-a", "backend", "feat-x")       // 実在 worktree
	referencedLog := d.Store.LogPath("repo-a", "backend", "gone-a") // LastLog が参照
	orphanLog := d.Store.LogPath("repo-a", "backend", "gone-b")     // 孤児
	foreign := filepath.Join(logsDir, "README.md")                  // 規則外
	write(liveLog)
	write(referencedLog)
	write(orphanLog)
	write(coreserver.PrevLogPath(orphanLog))
	write(foreign)
	if err := d.Store.Update(t.Context(), func(s *coreserver.State) (bool, error) {
		s.Repo("repo-a").Server("backend").LastLog = referencedLog
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}

	res, err := Doctor(t.Context(), d, true)
	if err != nil {
		t.Fatal(err)
	}
	orphans := findingsOf(res, "orphan_log")
	if len(orphans) != 2 {
		t.Fatalf("orphan findings = %+v", orphans)
	}
	for _, f := range orphans {
		if !f.Fixed {
			t.Fatalf("orphan should be fixed: %+v", f)
		}
	}
	for _, path := range []string{orphanLog, coreserver.PrevLogPath(orphanLog)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("orphan log %s should be deleted", path)
		}
	}
	for _, path := range []string{liveLog, referencedLog, foreign} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("log %s must survive: %v", path, err)
		}
	}
}

// チェック 5: rm -rf 残骸の worktree メタデータは報告され、--fix で prune される。
func TestDoctorPrunableWorktrees(t *testing.T) {
	reposDir := t.TempDir()
	worktreesDir := t.TempDir()
	repoA := testutil.CloneWithBranchNamed(t, reposDir, "main", "repo-a")
	target := filepath.Join(worktreesDir, "feat-x", "repo-a")
	addWorktree(t, repoA, "feat-x", target)
	// remove を経ずに rm -rf。
	if err := os.RemoveAll(filepath.Join(worktreesDir, "feat-x")); err != nil {
		t.Fatal(err)
	}
	d := newDeps(t, serverfake.New(), &config.File{}, reposDir, worktreesDir)

	res, err := Doctor(t.Context(), d, false)
	if err != nil {
		t.Fatal(err)
	}
	prunable := findingsOf(res, "prunable_worktrees")
	if len(prunable) != 1 || prunable[0].Repo != "repo-a" || !prunable[0].Fixable || prunable[0].Detail == "" {
		t.Fatalf("prunable = %+v", prunable)
	}

	res, err = Doctor(t.Context(), d, true)
	if err != nil {
		t.Fatal(err)
	}
	if prunable := findingsOf(res, "prunable_worktrees"); len(prunable) != 1 || !prunable[0].Fixed {
		t.Fatalf("prunable after fix = %+v", prunable)
	}
	// 修復後は発見なし。
	res, err = Doctor(t.Context(), d, false)
	if err != nil {
		t.Fatal(err)
	}
	if prunable := findingsOf(res, "prunable_worktrees"); len(prunable) != 0 {
		t.Fatalf("prunable should be gone: %+v", prunable)
	}
}

// チェック 6・7: 名ばかり .git、設定だけのリポジトリ、サーバー設定の無いリポジトリは
// 報告のみ（Fixable=false。--fix でも何も変更されない）。
func TestDoctorReportOnlyChecks(t *testing.T) {
	reposDir := t.TempDir()
	worktreesDir := t.TempDir()
	testutil.CloneWithBranchNamed(t, reposDir, "main", "repo-a")
	// 名ばかり .git（探索には引っかかるが git は管理していない）。
	if err := os.MkdirAll(filepath.Join(reposDir, "fake-repo", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.File{Repos: map[string]config.RepoConfig{
		"repo-a": {Servers: map[string]coreserver.Spec{"backend": {Start: cmdspec.FromString("run")}}},
		"ghost":  {Base: "develop"}, // 設定に居るが実在しない
	}}
	d := newDeps(t, serverfake.New(), cfg, reposDir, worktreesDir)

	res, err := Doctor(t.Context(), d, true)
	if err != nil {
		t.Fatal(err)
	}
	if broken := findingsOf(res, "broken_repo"); len(broken) != 1 || broken[0].Repo != "fake-repo" || broken[0].Fixable {
		t.Fatalf("broken_repo = %+v", broken)
	}
	if ghosts := findingsOf(res, "config_without_repo"); len(ghosts) != 1 || ghosts[0].Repo != "ghost" || ghosts[0].Fixable {
		t.Fatalf("config_without_repo = %+v", ghosts)
	}
	// repo-a はサーバー設定があるので現れず、fake-repo だけが現れる。
	if unconfigured := findingsOf(res, "repo_without_servers"); len(unconfigured) != 1 || unconfigured[0].Repo != "fake-repo" {
		t.Fatalf("repo_without_servers = %+v", unconfigured)
	}
	// 報告のみの発見は --fix でも Fixed にならない。
	if res.Fixed != 0 || res.FixFailed != 0 {
		t.Fatalf("report-only findings must not be fixed: %+v", res)
	}
}

// 何も問題が無ければ発見ゼロ（Findings は非 nil）。
func TestDoctorHealthy(t *testing.T) {
	reposDir := t.TempDir()
	worktreesDir := t.TempDir()
	repoA := testutil.CloneWithBranchNamed(t, reposDir, "main", "repo-a")
	addWorktree(t, repoA, "feat-x", filepath.Join(worktreesDir, "feat-x", "repo-a"))
	cfg := &config.File{Repos: map[string]config.RepoConfig{
		"repo-a": {Servers: map[string]coreserver.Spec{"backend": {Start: cmdspec.FromString("run")}}},
	}}
	d := newDeps(t, serverfake.New(), cfg, reposDir, worktreesDir)

	res, err := Doctor(t.Context(), d, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Findings == nil || len(res.Findings) != 0 {
		t.Fatalf("findings = %#v", res.Findings)
	}
}
