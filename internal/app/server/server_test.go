package server

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vimrak-hal/worktree-integrator/internal/app/action"
	corealias "github.com/vimrak-hal/worktree-integrator/internal/core/alias"
	"github.com/vimrak-hal/worktree-integrator/internal/core/cmdspec"
	coreserver "github.com/vimrak-hal/worktree-integrator/internal/core/server"
	"github.com/vimrak-hal/worktree-integrator/internal/core/server/serverfake"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/statedir"
)

type env struct {
	repos     string
	worktrees string
	state     string
}

func newEnv(t *testing.T, worktreeNames ...string) *env {
	t.Helper()
	e := &env{repos: t.TempDir(), worktrees: t.TempDir(), state: t.TempDir()}
	for _, wt := range worktreeNames {
		if err := os.MkdirAll(filepath.Join(e.worktrees, wt, "app"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func name(t *testing.T, raw string) action.Name {
	t.Helper()
	n, err := action.ParseName(raw)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func ptr[T any](v T) *T { return &v }

// servers は 2 サーバー構成の設定。backend はライフサイクルコマンドを持たず
// （フォアグラウンド実行なし = FailForeground の影響を受けない）、frontend は setup を
// 持つ。部分失敗（backend は成功・frontend は失敗）を組み立てられる形にしてある。
func servers() coreserver.Config {
	return coreserver.Config{"app": coreserver.RepoServers{
		"backend":  coreserver.Spec{Start: cmdspec.FromString("run-backend")},
		"frontend": coreserver.Spec{Start: cmdspec.FromString("run-frontend"), Setup: ptr(cmdspec.FromString("install"))},
	}}
}

func (e *env) root() statedir.Root { return statedir.At(e.state) }

func (e *env) store() *coreserver.StateStore { return coreserver.NewStateStore(e.root()) }

func (e *env) aliases() *corealias.Store { return corealias.NewStore(e.root()) }

// deps はワークフローの依存の束を、このテスト環境の状態ルートから構築する。
// Events は nil（無通知）— ワークフローが io.Writer にもコールバックにも依存せず
// Result を返すことを、そのまま検証する形になる。
func (e *env) deps(proc coreserver.ProcessControl) Deps {
	return Deps{Proc: proc, Store: e.store(), Aliases: e.aliases(), Root: e.root()}
}

func (e *env) cmd() action.ServerCommand {
	return action.ServerCommand{ReposDir: e.repos, WorktreesDir: e.worktrees, Servers: servers()}
}

func (e *env) switchTo(t *testing.T, proc coreserver.ProcessControl, wt string) *SwitchResult {
	t.Helper()
	res, err := Switch(t.Context(), e.deps(proc), e.cmd(), action.SwitchKind{Name: name(t, wt)})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// loadState は共有セッション（View）で状態を読み込むテストヘルパー。
func loadState(t *testing.T, store *coreserver.StateStore) *coreserver.State {
	t.Helper()
	var loaded *coreserver.State
	if err := store.View(t.Context(), func(s *coreserver.State) error {
		loaded = s
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return loaded
}

func TestSwitchStartsAllServersThenSwitchesRepo(t *testing.T) {
	e := newEnv(t, "feat-a", "feat-b")
	proc := serverfake.New()

	res := e.switchTo(t, proc, "feat-a")
	if res.Started != 2 || res.Already != 0 || res.Skipped != 0 || res.Failed != 0 {
		t.Fatalf("res = %+v", res)
	}
	if len(res.PerServer) != 2 || res.PerServer[0].Status != OutcomeStarted {
		t.Fatalf("per-server = %+v", res.PerServer)
	}
	// イベントは Result に構造化されて残る（started と pid）。
	if evs := res.PerServer[0].Events; len(evs) == 0 || evs[len(evs)-1].Kind != "started" || evs[len(evs)-1].Pid == 0 {
		t.Fatalf("events = %+v", evs)
	}
	s := loadState(t, e.store())
	be := s.Repos["app"].Servers["backend"]
	if be.Running == nil || be.Running.Worktree != "feat-a" {
		t.Fatalf("backend should run on feat-a: %+v", be.Running)
	}
	first := be.Running.Ident
	if !proc.Alive(first) {
		t.Fatal("backend should be running")
	}

	e.switchTo(t, proc, "feat-b")
	s2 := loadState(t, e.store())
	be2 := s2.Repos["app"].Servers["backend"]
	if be2.Running == nil || be2.Running.Worktree != "feat-b" {
		t.Fatalf("backend should run on feat-b: %+v", be2.Running)
	}
	if proc.Alive(first) {
		t.Fatal("old backend should be stopped")
	}
}

func TestMissingWorktreeSkippedByDefault(t *testing.T) {
	e := newEnv(t) // worktree ディレクトリなし
	res := e.switchTo(t, serverfake.New(), "feat-a")
	if res.Skipped != 2 || res.Started != 0 {
		t.Fatalf("res = %+v", res)
	}
	for _, o := range res.PerServer {
		if o.Status != OutcomeSkipped || o.Reason != ReasonMissingWorktree || o.Path == "" {
			t.Fatalf("outcome = %+v", o)
		}
	}
}

func TestRequireWorktreeErrorsOnMissing(t *testing.T) {
	e := newEnv(t)
	_, err := Switch(t.Context(), e.deps(serverfake.New()), e.cmd(),
		action.SwitchKind{Name: name(t, "feat-a"), RequireWorktree: true})
	if err == nil || !strings.Contains(err.Error(), "worktree が見つかりません") {
		t.Fatalf("err = %v", err)
	}
}

func TestStatusShowsWorktreePerRowThenStopClears(t *testing.T) {
	e := newEnv(t, "feat-a")
	proc := serverfake.New()
	e.switchTo(t, proc, "feat-a")

	res, err := Status(t.Context(), e.deps(proc), e.cmd())
	if err != nil {
		t.Fatal(err)
	}
	// 行は（repo × server）ごと。WORKTREE は稼働インスタンスの属性からの導出値。
	if len(res.Rows) != 2 {
		t.Fatalf("rows = %+v", res.Rows)
	}
	for _, row := range res.Rows {
		if row.Repo != "app" || row.Worktree != "feat-a" || row.State != StateRunning || row.Pid == 0 {
			t.Fatalf("row = %+v", row)
		}
	}

	stopRes, err := Stop(t.Context(), e.deps(proc), e.cmd(), action.StopKind{Scope: action.AllWorktrees{}})
	if err != nil {
		t.Fatal(err)
	}
	if stopRes.Stopped != 2 || stopRes.Failed != 0 {
		t.Fatalf("stop = %+v", stopRes)
	}
	after := loadState(t, e.store())
	if after.Repos["app"].Servers["backend"].Running != nil {
		t.Fatal("backend running should be cleared")
	}
	if after.Repos["app"].Servers["frontend"].Running != nil {
		t.Fatal("frontend running should be cleared")
	}
}

func TestStatusShowsAlias(t *testing.T) {
	e := newEnv(t, "feat-a")
	proc := serverfake.New()
	e.switchTo(t, proc, "feat-a")

	if _, err := e.aliases().Set(t.Context(), "feat-a", "ABC-123: Fix login"); err != nil {
		t.Fatal(err)
	}

	res, err := Status(t.Context(), e.deps(proc), e.cmd())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range res.Rows {
		if row.Alias != "ABC-123: Fix login" {
			t.Fatalf("row = %+v", row)
		}
	}
}

// 部分失敗: backend（ライフサイクルなし）は新 worktree へ移り、frontend（setup が
// 失敗）は旧 worktree で稼働し続ける。status はその現実をそのまま行ごとに返す。
func TestPartialFailureLeavesMixedWorktrees(t *testing.T) {
	e := newEnv(t, "feat-a", "feat-b")
	proc := serverfake.New()
	e.switchTo(t, proc, "feat-a")

	// 以降のフォアグラウンドコマンドは失敗する → frontend の feat-b setup が失敗。
	proc.FailForeground()
	res, err := Switch(t.Context(), e.deps(proc), e.cmd(), action.SwitchKind{Name: name(t, "feat-b")})
	if err == nil || !strings.Contains(err.Error(), "1 件のサーバー操作に失敗しました") {
		t.Fatalf("switch err = %v", err)
	}
	// 部分結果は保持され、失敗の詳細は型付きで残る。
	if res == nil || res.Started != 1 || res.Failed != 1 {
		t.Fatalf("res = %+v", res)
	}
	var failed *ServerOutcome
	for i := range res.PerServer {
		if res.PerServer[i].Status == OutcomeFailed {
			failed = &res.PerServer[i]
		}
	}
	if failed == nil || failed.Server != "frontend" || failed.Failure == nil || failed.Failure.Kind != FailStep || failed.Failure.Step != "setup" {
		t.Fatalf("failed outcome = %+v", failed)
	}

	s := loadState(t, e.store())
	be := s.Repos["app"].Servers["backend"].Running
	fe := s.Repos["app"].Servers["frontend"].Running
	if be == nil || be.Worktree != "feat-b" {
		t.Fatalf("backend should have switched to feat-b: %+v", be)
	}
	if fe == nil || fe.Worktree != "feat-a" {
		t.Fatalf("frontend must keep running on feat-a: %+v", fe)
	}
	if !proc.Alive(fe.Ident) {
		t.Fatal("frontend old instance must still be alive")
	}

	// status に混在がそのまま出る（backend 行に feat-b、frontend 行に feat-a）。
	statusRes, err := Status(t.Context(), e.deps(proc), e.cmd())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, row := range statusRes.Rows {
		got[row.Server] = row.Worktree
	}
	if got["backend"] != "feat-b" || got["frontend"] != "feat-a" {
		t.Fatalf("status should show the mixed reality: %+v", statusRes.Rows)
	}
}

// stop <name> は「その worktree で動いているサーバーを止める」: 混在状態から feat-a を
// 指定すると frontend だけが止まる。
func TestStopOneWorktreeStopsOnlyMatchingServers(t *testing.T) {
	e := newEnv(t, "feat-a", "feat-b")
	proc := serverfake.New()
	e.switchTo(t, proc, "feat-a")
	proc.FailForeground()
	_, _ = Switch(t.Context(), e.deps(proc), e.cmd(), action.SwitchKind{Name: name(t, "feat-b")}) // backend→feat-b, frontend は feat-a のまま

	res, err := Stop(t.Context(), e.deps(proc), e.cmd(), action.StopKind{Scope: action.OneWorktree{Name: name(t, "feat-a")}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stopped != 1 || res.Failed != 0 {
		t.Fatalf("res = %+v", res)
	}
	if len(res.PerServer) != 1 || res.PerServer[0].Server != "frontend" || res.PerServer[0].Status != OutcomeStopped {
		t.Fatalf("per-server = %+v", res.PerServer)
	}

	s := loadState(t, e.store())
	if s.Repos["app"].Servers["frontend"].Running != nil {
		t.Fatal("frontend (feat-a) should be stopped")
	}
	be := s.Repos["app"].Servers["backend"].Running
	if be == nil || be.Worktree != "feat-b" || !proc.Alive(be.Ident) {
		t.Fatalf("backend (feat-b) must keep running: %+v", be)
	}
}

// 停止失敗: Running は台帳に残り、stop はエラーで失敗を観測可能にする。
func TestStopFailureKeepsRecordAndErrors(t *testing.T) {
	e := newEnv(t, "feat-a")
	proc := serverfake.New()
	e.switchTo(t, proc, "feat-a")

	proc.StopError = errors.New("still alive after SIGKILL")
	res, err := Stop(t.Context(), e.deps(proc), e.cmd(), action.StopKind{Scope: action.AllWorktrees{}})
	if err == nil || !strings.Contains(err.Error(), "2 件のサーバー停止に失敗しました") {
		t.Fatalf("stop err = %v", err)
	}
	if res == nil || res.Failed != 2 || res.Stopped != 0 {
		t.Fatalf("res = %+v", res)
	}
	for _, o := range res.PerServer {
		if o.Status != OutcomeStopFailed {
			t.Fatalf("outcome = %+v", o)
		}
		// 失敗の詳細はイベント（stop_failed + エラー文字列）として残る。
		last := o.Events[len(o.Events)-1]
		if last.Kind != "stop_failed" || !strings.Contains(last.Error, "still alive") {
			t.Fatalf("events = %+v", o.Events)
		}
	}

	s := loadState(t, e.store())
	for _, sv := range []string{"backend", "frontend"} {
		running := s.Repos["app"].Servers[sv].Running
		if running == nil {
			t.Fatalf("%s の Running は保持されるべき（孤児を台帳から消さない）", sv)
		}
		if !proc.Alive(running.Ident) {
			t.Fatalf("%s should still be alive", sv)
		}
	}
}

// writeLog はフェイクが書かないログファイルをテストが用意する。
func writeLog(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// linesOf は LogsResult から repo/server のエントリの行を返す。
func linesOf(t *testing.T, res *LogsResult, repo, server string) []string {
	t.Helper()
	for _, e := range res.Logs {
		if e.Repo == repo && e.Server == server {
			return e.Lines
		}
	}
	t.Fatalf("entry %s/%s missing: %+v", repo, server, res.Logs)
	return nil
}

func TestLogsReadsNamedWorktreeLog(t *testing.T) {
	e := newEnv(t, "feat-a")
	store := e.store()
	proc := serverfake.New()
	e.switchTo(t, proc, "feat-a")
	// フェイクは実 FS に書かないため、稼働インスタンスのログはテストが注入する。
	writeLog(t, store.LogPath("app", "backend", "feat-a"), "log for run-backend\n")
	writeLog(t, store.LogPath("app", "frontend", "feat-a"), "log for run-frontend\n")

	cmd := e.cmd()
	cmd.Repos = []string{"app"}
	res, err := Logs(t.Context(), e.deps(proc), cmd, action.LogsKind{Scope: action.OneWorktree{Name: name(t, "feat-a")}, Lines: 50})
	if err != nil {
		t.Fatal(err)
	}
	if got := linesOf(t, res, "app", "backend"); len(got) != 1 || got[0] != "log for run-backend" {
		t.Fatalf("backend lines = %v", got)
	}
	if got := linesOf(t, res, "app", "frontend"); len(got) != 1 || got[0] != "log for run-frontend" {
		t.Fatalf("frontend lines = %v", got)
	}
}

// Lines が 0 以下ならパスの解決だけを返し、本文は読まない（TUI が増分読みのために
// パスだけを定期取得する経路）。Missing の判定は通常どおり行われる。
func TestLogsZeroLinesResolvesPathsOnly(t *testing.T) {
	e := newEnv(t, "feat-a")
	store := e.store()
	proc := serverfake.New()
	e.switchTo(t, proc, "feat-a")
	writeLog(t, store.LogPath("app", "backend", "feat-a"), "should not be read\n")

	res, err := Logs(t.Context(), e.deps(proc), e.cmd(),
		action.LogsKind{Scope: action.OneWorktree{Name: name(t, "feat-a")}, Lines: 0})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range res.Logs {
		if entry.Path == "" {
			t.Fatalf("entry must resolve a path: %+v", entry)
		}
		if len(entry.Lines) != 0 || entry.Error != "" {
			t.Fatalf("no content must be read with Lines<=0: %+v", entry)
		}
		// Missing 判定は Lines=0 でも生きている: 注入した backend のログだけが
		// 実在し、frontend のログは存在しない。
		if wantMissing := entry.Server != "backend"; entry.Missing != wantMissing {
			t.Fatalf("missing detection must still apply: %+v", entry)
		}
	}
}

// logs --prev は 1 世代前（.prev）を読む。
func TestLogsPrevReadsRotatedGeneration(t *testing.T) {
	e := newEnv(t, "feat-a")
	store := e.store()
	proc := serverfake.New()
	e.switchTo(t, proc, "feat-a")
	current := store.LogPath("app", "backend", "feat-a")
	writeLog(t, current, "current generation\n")
	writeLog(t, coreserver.PrevLogPath(current), "previous generation\n")

	res, err := Logs(t.Context(), e.deps(proc), e.cmd(),
		action.LogsKind{Scope: action.OneWorktree{Name: name(t, "feat-a")}, Lines: 50, Prev: true})
	if err != nil {
		t.Fatal(err)
	}
	got := linesOf(t, res, "app", "backend")
	if len(got) != 1 || got[0] != "previous generation" {
		t.Fatalf("--prev must read the rotated generation: %v", got)
	}
}

// logs（名前なし）は Running 不在でも最後に稼働したログ（LastLog）へフォールバック
// する（クラッシュ・停止直後に原因ログが見えるように）。
func TestLogsFallsBackToLastLogWhenNotRunning(t *testing.T) {
	e := newEnv(t, "feat-a")
	store := e.store()
	proc := serverfake.New()
	e.switchTo(t, proc, "feat-a")
	writeLog(t, store.LogPath("app", "backend", "feat-a"), "last words of backend\n")
	writeLog(t, store.LogPath("app", "frontend", "feat-a"), "last words of frontend\n")

	// 停止すると Running は消えるが LastLog は残る。
	if _, err := Stop(t.Context(), e.deps(proc), e.cmd(), action.StopKind{Scope: action.AllWorktrees{}}); err != nil {
		t.Fatal(err)
	}

	res, err := Logs(t.Context(), e.deps(proc), e.cmd(), action.LogsKind{Scope: action.AllWorktrees{}, Lines: 50})
	if err != nil {
		t.Fatal(err)
	}
	if got := linesOf(t, res, "app", "backend"); len(got) != 1 || got[0] != "last words of backend" {
		t.Fatalf("logs should fall back to LastLog: %v", got)
	}
}

// 名前指定スコープでは、存在しないログも Missing エントリとして結果に現れる。
func TestLogsNamedScopeReportsMissing(t *testing.T) {
	e := newEnv(t, "feat-a")
	res, err := Logs(t.Context(), e.deps(serverfake.New()), e.cmd(),
		action.LogsKind{Scope: action.OneWorktree{Name: name(t, "feat-a")}, Lines: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Logs) != 2 {
		t.Fatalf("logs = %+v", res.Logs)
	}
	for _, entry := range res.Logs {
		if !entry.Missing || entry.Path == "" {
			t.Fatalf("entry = %+v", entry)
		}
	}
}

// Repos フィルタは ServerCommand のフィールドであり、全操作に共通に効く。
// 設定にも状態にも無い名前は UnknownRepos として返る（エラーではない）。stop でも同様。
func TestRepoFilterReportsUnknown(t *testing.T) {
	e := newEnv(t, "feat-a")
	cmd := e.cmd()
	cmd.Repos = []string{"nope"}

	statusRes, err := Status(t.Context(), e.deps(serverfake.New()), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if len(statusRes.UnknownRepos) != 1 || statusRes.UnknownRepos[0] != "nope" {
		t.Fatalf("status unknown = %+v", statusRes.UnknownRepos)
	}

	stopRes, err := Stop(t.Context(), e.deps(serverfake.New()), cmd, action.StopKind{Scope: action.AllWorktrees{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(stopRes.UnknownRepos) != 1 || stopRes.UnknownRepos[0] != "nope" {
		t.Fatalf("stop unknown = %+v", stopRes.UnknownRepos)
	}
}

// サーバー設定が空のときは NoServerConfig が立つ（表示層が [servers.*] を案内する）。
func TestStatusReportsNoServerConfig(t *testing.T) {
	e := newEnv(t)
	cmd := action.ServerCommand{ReposDir: e.repos, WorktreesDir: e.worktrees, Servers: coreserver.Config{}}
	res, err := Status(t.Context(), e.deps(serverfake.New()), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if !res.NoServerConfig || len(res.Rows) != 0 {
		t.Fatalf("res = %+v", res)
	}
}

// 設定から消えたが稼働記録の残るサーバーは、和集合の対象解決により status に現れ、
// stop で停止できる。switch はそれをスキップとして報告する。
func TestStateOnlyServerVisibleAndStoppable(t *testing.T) {
	e := newEnv(t, "feat-a")
	store := e.store()
	proc := serverfake.New()
	// 設定に無いリポジトリ legacy のサーバー old が稼働している状態を作る。
	id, err := proc.SpawnDetached("run-old", "/cwd", nil, "/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(t.Context(), func(s *coreserver.State) (bool, error) {
		s.Repo("legacy").Servers["old"] = &coreserver.Runtime{
			Running: &coreserver.Instance{Ident: id, Worktree: "feat-z", Log: "/dev/null", GraceSecs: 5},
		}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}

	statusRes, err := Status(t.Context(), e.deps(proc), e.cmd())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range statusRes.Rows {
		if row.Repo == "legacy" && row.Server == "old" && row.Worktree == "feat-z" && row.State == StateRunning {
			found = true
		}
	}
	if !found {
		t.Fatalf("state-only server missing from status: %+v", statusRes.Rows)
	}

	// switch は設定の無いサーバーをスキップとして報告する。
	switchRes := e.switchTo(t, proc, "feat-a")
	skipped := false
	for _, o := range switchRes.PerServer {
		if o.Repo == "legacy" && o.Status == OutcomeSkipped && o.Reason == ReasonNoServerConfig {
			skipped = true
		}
	}
	if !skipped {
		t.Fatalf("state-only server should be reported as skipped: %+v", switchRes.PerServer)
	}

	if _, err := Stop(t.Context(), e.deps(proc), e.cmd(), action.StopKind{Scope: action.AllWorktrees{}}); err != nil {
		t.Fatal(err)
	}
	if proc.Alive(id) {
		t.Fatal("state-only server should be stopped")
	}
	if loadState(t, store).Repos["legacy"].Servers["old"].Running != nil {
		t.Fatal("record should be cleared")
	}
}

// 並行する switch と stop（CLI と MCP の同時実行に相当）が repo 操作ロックで直列化
// され、-race のもとでも状態が現実と整合したまま終わることを確認する。それぞれが
// 独立のストアインスタンス（同じ状態ルート）を使う。
func TestConcurrentSwitchAndStopAreSerialized(t *testing.T) {
	e := newEnv(t, "feat-a", "feat-b")
	proc := serverfake.New()
	e.switchTo(t, proc, "feat-a")

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errs[0] = Switch(t.Context(), e.deps(proc), e.cmd(), action.SwitchKind{Name: name(t, "feat-b")})
	}()
	go func() {
		defer wg.Done()
		_, errs[1] = Stop(t.Context(), e.deps(proc), e.cmd(), action.StopKind{Scope: action.AllWorktrees{}})
	}()
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}

	// どちらの順で直列化されても、台帳は現実（フェイクの生存状態）と一致している:
	// Running が記録されていればそのプロセスは生きており、記録が無ければ対応する
	// プロセスは残っていない。
	s := loadState(t, e.store())
	for sv, runtime := range s.Repos["app"].Servers {
		if runtime.Running != nil && !proc.Alive(runtime.Running.Ident) {
			t.Fatalf("%s: recorded instance is not alive: %+v", sv, runtime.Running)
		}
	}
}
