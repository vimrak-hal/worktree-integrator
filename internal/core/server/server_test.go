package server_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vimrak-hal/worktree-integrator/internal/core/cmdspec"
	"github.com/vimrak-hal/worktree-integrator/internal/core/server"
	"github.com/vimrak-hal/worktree-integrator/internal/core/server/serverfake"
	"github.com/vimrak-hal/worktree-integrator/internal/core/wtenv"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/proc"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/statedir"
)

func ptr[T any](v T) *T { return &v }

// newStore は一時ディレクトリを状態ルートとする StateStore を返す。
func newStore(t *testing.T) *server.StateStore {
	t.Helper()
	return server.NewStateStore(statedir.At(t.TempDir()))
}

// saveState は排他セッションで状態を保存するテストヘルパー。
func saveState(t *testing.T, store *server.StateStore, state *server.State) {
	t.Helper()
	session, err := store.Exclusive(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	if err := session.Save(state); err != nil {
		t.Fatal(err)
	}
}

// loadState は共有セッション（View）で状態を読み込むテストヘルパー。
func loadState(t *testing.T, store *server.StateStore) *server.State {
	t.Helper()
	var loaded *server.State
	if err := store.View(t.Context(), func(s *server.State) error {
		loaded = s
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return loaded
}

// ----- 設定 -----

func TestGraceDefaultsAndOverride(t *testing.T) {
	def := server.Spec{Start: cmdspec.FromString("x")}
	if def.Grace() != time.Duration(server.DefaultStopGraceSecs)*time.Second {
		t.Fatalf("default grace = %v", def.Grace())
	}
	custom := server.Spec{Start: cmdspec.FromString("x"), StopGraceSecs: ptr(uint64(10))}
	if custom.Grace() != 10*time.Second {
		t.Fatalf("custom grace = %v", custom.Grace())
	}
}

func TestServersConfigIsEmpty(t *testing.T) {
	cfg := server.Config{}
	if !cfg.IsEmpty() {
		t.Fatal("empty config should be empty")
	}
	cfg = server.Config{"app": server.RepoServers{"backend": server.Spec{Start: cmdspec.FromString("x")}}}
	if cfg.IsEmpty() {
		t.Fatal("config with a server is not empty")
	}
}

// ----- 状態（v2 の TOML 往復） -----

func TestStateRoundTripAndOmitEmpty(t *testing.T) {
	store := newStore(t)
	log := store.LogPath("rails", "backend", "feat-a")
	state := &server.State{Version: server.StateVersion, Repos: map[string]*server.RepoState{
		"rails": {
			Servers: map[string]*server.Runtime{
				"backend": {
					LastLog: log,
					Setup: map[string]server.SetupRecord{
						"feat-a": {Path: "/worktrees/feat-a/rails", DoneAt: 1718880000},
					},
					Running: &server.Instance{
						Ident:     proc.Ident{Pid: 4321, Pgid: 4321, StartUnixMs: 1718880600123},
						Worktree:  "feat-a",
						Log:       log,
						GraceSecs: 7,
						StartedAt: 1718880662,
					},
				},
				"frontend": {Setup: map[string]server.SetupRecord{"feat-a": {Path: "/worktrees/feat-a/rails"}}},
			},
		},
	}}
	saveState(t, store, state)
	loaded := loadState(t, store)
	be := loaded.Repos["rails"].Servers["backend"]
	if be.Running == nil || be.Running.Ident.Pid != 4321 || be.Running.Ident.StartUnixMs != 1718880600123 {
		t.Fatalf("running = %+v", be.Running)
	}
	if be.Running.Worktree != "feat-a" {
		t.Fatalf("worktree = %q", be.Running.Worktree)
	}
	if be.Running.GraceSecs != 7 || be.Running.Grace() != 7*time.Second {
		t.Fatalf("grace = %d", be.Running.GraceSecs)
	}
	if be.LastLog != log {
		t.Fatalf("last_log = %q", be.LastLog)
	}
	if rec := be.Setup["feat-a"]; rec.Path != "/worktrees/feat-a/rails" || rec.DoneAt != 1718880000 {
		t.Fatalf("setup record = %+v", rec)
	}
	if loaded.Repos["rails"].Servers["frontend"].Running != nil {
		t.Fatal("frontend running should be nil")
	}
}

func TestStateOmitsEmptyFields(t *testing.T) {
	store := newStore(t)
	state := &server.State{Version: server.StateVersion, Repos: map[string]*server.RepoState{
		"app": {Servers: map[string]*server.Runtime{
			"backend": {},
		}},
	}}
	saveState(t, store, state)
	data, _ := os.ReadFile(store.StateFile())
	for _, forbidden := range []string{"running", "last_log", "setup", "active", "initialized"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("%q should be omitted: %s", forbidden, data)
		}
	}
}

// ----- アクティベート -----

// testEnv は SwitchServer テスト用の実ディレクトリ一式。isFirst 判定が
// SetupRecord.Path の実在を見るため、worktree のリポジトリルートは実在させる。
type testEnv struct {
	worktrees string
	logs      string
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	return &testEnv{worktrees: t.TempDir(), logs: t.TempDir()}
}

// run は worktree 名の RunContext を返す。
func (e *testEnv) run(name string) *wtenv.RunContext {
	return wtenv.NewRunContext(name, "/repos", e.worktrees)
}

// repo は worktree 内のリポジトリルート（実在するディレクトリ）を持つ RepoContext を返す。
func (e *testEnv) repo(t *testing.T, repo, worktree string) *wtenv.RepoContext {
	t.Helper()
	path := filepath.Join(e.worktrees, worktree, repo)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return &wtenv.RepoContext{RepoName: repo, RepoPath: filepath.Join("/repos", repo), WorktreePath: path}
}

func (e *testEnv) log(repo, server, worktree string) string {
	return filepath.Join(e.logs, repo+"__"+server+"__"+worktree+".log")
}

func specWithSetup() server.Spec {
	return server.Spec{Start: cmdspec.FromString("run-server"), Setup: ptr(cmdspec.FromString("install"))}
}

func (e *testEnv) switchReq(t *testing.T, repo, worktree string, spec server.Spec, restart bool) *server.SwitchRequest {
	t.Helper()
	repoCtx := e.repo(t, repo, worktree)
	return &server.SwitchRequest{
		Run: e.run(worktree), Repo: repoCtx, ServerName: "backend", Spec: spec,
		ServerCwd: repoCtx.WorktreePath, Log: e.log(repo, "backend", worktree),
		Restart: restart,
	}
}

func TestFirstActivationRunsSetupAndStarts(t *testing.T) {
	e := newTestEnv(t)
	fake := serverfake.New()
	runtime := &server.Runtime{}
	spec := specWithSetup()

	result, err := server.SwitchServer(t.Context(), fake, runtime, e.switchReq(t, "app", "feat-a", spec, false))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != server.SwitchStarted {
		t.Fatalf("status = %v", result.Status)
	}
	rec, ok := runtime.Setup["feat-a"]
	if !ok {
		t.Fatalf("setup record missing: %+v", runtime.Setup)
	}
	if rec.Path != filepath.Join(e.worktrees, "feat-a", "app") || rec.DoneAt == 0 {
		t.Fatalf("setup record = %+v", rec)
	}
	if runtime.Running == nil {
		t.Fatal("running should be set")
	}
	if runtime.Running.Worktree != "feat-a" {
		t.Fatalf("Instance.Worktree = %q", runtime.Running.Worktree)
	}
	if runtime.Running.GraceSecs != server.DefaultStopGraceSecs {
		t.Fatalf("GraceSecs = %d (spec の Grace を凍結すべき)", runtime.Running.GraceSecs)
	}
	if runtime.LastLog != e.log("app", "backend", "feat-a") {
		t.Fatalf("LastLog = %q", runtime.LastLog)
	}
	if got := fake.ForegroundRuns(); len(got) != 1 || got[0] != "install" {
		t.Fatalf("foreground = %v", got)
	}
	if got := fake.Spawns(); len(got) != 1 {
		t.Fatalf("spawns = %v", got)
	}
}

func TestGraceSecsFrozenFromSpec(t *testing.T) {
	e := newTestEnv(t)
	fake := serverfake.New()
	runtime := &server.Runtime{}
	spec := server.Spec{Start: cmdspec.FromString("run"), StopGraceSecs: ptr(uint64(7))}

	if _, err := server.SwitchServer(t.Context(), fake, runtime, e.switchReq(t, "app", "feat-a", spec, false)); err != nil {
		t.Fatal(err)
	}
	if runtime.Running.GraceSecs != 7 {
		t.Fatalf("GraceSecs = %d, want 7", runtime.Running.GraceSecs)
	}

	// 停止は（その後 Spec が変わっていても）凍結された値を使う。
	res := server.StopServer(t.Context(), fake, runtime)
	if !res.Stopped {
		t.Fatalf("stop result = %+v", res)
	}
	if fake.LastGrace() != 7*time.Second {
		t.Fatalf("StopGroup grace = %v, want 7s (Instance.GraceSecs)", fake.LastGrace())
	}
}

func TestAlreadyRunningTargetIsNoOpWithoutRestart(t *testing.T) {
	e := newTestEnv(t)
	fake := serverfake.New()
	runtime := &server.Runtime{}
	spec := specWithSetup()

	if _, err := server.SwitchServer(t.Context(), fake, runtime, e.switchReq(t, "app", "feat-a", spec, false)); err != nil {
		t.Fatal(err)
	}
	result, err := server.SwitchServer(t.Context(), fake, runtime, e.switchReq(t, "app", "feat-a", spec, false))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != server.SwitchAlreadyRunning {
		t.Fatalf("status = %v", result.Status)
	}
	if len(result.Events) != 1 || result.Events[0].Kind != server.EventAlreadyRunning {
		t.Fatalf("events = %+v", result.Events)
	}
	if got := fake.Spawns(); len(got) != 1 {
		t.Fatalf("should not spawn again: %v", got)
	}
}

func TestRestartStopsAndStartsSameWorktree(t *testing.T) {
	e := newTestEnv(t)
	fake := serverfake.New()
	runtime := &server.Runtime{}
	spec := specWithSetup()

	if _, err := server.SwitchServer(t.Context(), fake, runtime, e.switchReq(t, "app", "feat-a", spec, false)); err != nil {
		t.Fatal(err)
	}
	first := runtime.Running.Ident

	result, err := server.SwitchServer(t.Context(), fake, runtime, e.switchReq(t, "app", "feat-a", spec, true))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != server.SwitchStarted {
		t.Fatalf("status = %v", result.Status)
	}
	if fake.Alive(first) {
		t.Fatal("restart should stop the previous instance")
	}
	if runtime.Running.Ident == first {
		t.Fatal("restart should start a new instance")
	}
}

func TestSwitchingWorktreesStopsPreviousAfterLifecycle(t *testing.T) {
	e := newTestEnv(t)
	fake := serverfake.New()
	runtime := &server.Runtime{}
	spec := specWithSetup()

	if _, err := server.SwitchServer(t.Context(), fake, runtime, e.switchReq(t, "app", "feat-a", spec, false)); err != nil {
		t.Fatal(err)
	}
	first := runtime.Running.Ident

	result, err := server.SwitchServer(t.Context(), fake, runtime, e.switchReq(t, "app", "feat-b", spec, false))
	if err != nil {
		t.Fatal(err)
	}
	second := runtime.Running.Ident
	if first == second {
		t.Fatal("expected a new instance")
	}
	if fake.Alive(first) {
		t.Fatal("old instance should be stopped")
	}
	if !fake.Alive(second) {
		t.Fatal("new instance should be running")
	}
	if runtime.Running.Worktree != "feat-b" {
		t.Fatalf("worktree = %q", runtime.Running.Worktree)
	}
	// 順序の契約: 旧停止（StoppingOld → Stopped）はライフサイクル成功後・起動
	// （Started）の直前。EventStopped は StopGroup の成功後にのみ発行される。
	kinds := eventKinds(result.Events)
	want := []server.EventKind{server.EventStoppingOld, server.EventStopped, server.EventStarted}
	if len(kinds) != len(want) {
		t.Fatalf("events = %v", kinds)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("events = %v, want %v", kinds, want)
		}
	}
}

func eventKinds(events []server.Event) []server.EventKind {
	kinds := make([]server.EventKind, len(events))
	for i, ev := range events {
		kinds[i] = ev.Kind
	}
	return kinds
}

func TestFailedSetupDoesNotStartAndKeepsOldRunning(t *testing.T) {
	e := newTestEnv(t)
	fake := serverfake.New()
	runtime := &server.Runtime{}
	spec := specWithSetup()

	// feat-a で稼働開始。
	if _, err := server.SwitchServer(t.Context(), fake, runtime, e.switchReq(t, "app", "feat-a", spec, false)); err != nil {
		t.Fatal(err)
	}
	old := runtime.Running.Ident

	// 以降のライフサイクルコマンドは失敗する。feat-b への切り替えは setup（初回）で
	// 失敗し、旧インスタンスは停止されないまま生き続ける（停止はライフサイクル
	// 成功後に遅延されている）。
	fake.FailForeground()
	_, err := server.SwitchServer(t.Context(), fake, runtime, e.switchReq(t, "app", "feat-b", spec, false))
	var stepErr *server.StepError
	if !errors.As(err, &stepErr) {
		t.Fatalf("err = %v (%T), want *server.StepError", err, err)
	}
	if stepErr.Step != "setup" {
		t.Fatalf("step = %q, want setup", stepErr.Step)
	}
	if _, ok := runtime.Setup["feat-b"]; ok {
		t.Fatal("setup failed -> feat-b must not be recorded")
	}
	if runtime.Running == nil || runtime.Running.Ident != old {
		t.Fatalf("old instance record must be preserved: %+v", runtime.Running)
	}
	if !fake.Alive(old) {
		t.Fatal("old instance must still be alive (stop is deferred until lifecycle success)")
	}
	if got := fake.Spawns(); len(got) != 1 {
		t.Fatalf("no new spawn on failure: %v", got)
	}
}

// isFirst 判定は「Setup に記録があり、かつ record.Path が現存する」。worktree を
// 削除して同名で作り直した場合（Path 消滅）は setup が再実行される。
func TestSetupRerunsWhenRecordedPathIsGone(t *testing.T) {
	e := newTestEnv(t)
	fake := serverfake.New()
	spec := specWithSetup()
	runtime := &server.Runtime{
		Setup: map[string]server.SetupRecord{
			"feat-a": {Path: filepath.Join(e.worktrees, "nonexistent", "app"), DoneAt: 1},
		},
	}

	if _, err := server.SwitchServer(t.Context(), fake, runtime, e.switchReq(t, "app", "feat-a", spec, false)); err != nil {
		t.Fatal(err)
	}
	if got := fake.ForegroundRuns(); len(got) != 1 || got[0] != "install" {
		t.Fatalf("setup should re-run when the recorded path is gone, ran %v", got)
	}
	// 記録は現存するパスへ更新される。
	if rec := runtime.Setup["feat-a"]; rec.Path != filepath.Join(e.worktrees, "feat-a", "app") {
		t.Fatalf("setup record path = %q", rec.Path)
	}
}

// 記録のパスが現存すれば setup はスキップされ、on_switch が実行される。
func TestSetupSkippedWhenRecordedPathExists(t *testing.T) {
	e := newTestEnv(t)
	fake := serverfake.New()
	spec := specWithSetup()
	spec.OnSwitch = ptr(cmdspec.FromString("migrate"))
	repoCtx := e.repo(t, "app", "feat-a") // 実在させる
	runtime := &server.Runtime{
		Setup: map[string]server.SetupRecord{
			"feat-a": {Path: repoCtx.WorktreePath, DoneAt: 1},
		},
	}

	if _, err := server.SwitchServer(t.Context(), fake, runtime, e.switchReq(t, "app", "feat-a", spec, false)); err != nil {
		t.Fatal(err)
	}
	if got := fake.ForegroundRuns(); len(got) != 1 || got[0] != "migrate" {
		t.Fatalf("on_switch (not setup) should run, ran %v", got)
	}
}

// 停止失敗は switch の失敗: Running は保持され、新プロセスは起動されない。
func TestStopFailureAbortsSwitchAndKeepsRunning(t *testing.T) {
	e := newTestEnv(t)
	fake := serverfake.New()
	runtime := &server.Runtime{}
	spec := specWithSetup()

	if _, err := server.SwitchServer(t.Context(), fake, runtime, e.switchReq(t, "app", "feat-a", spec, false)); err != nil {
		t.Fatal(err)
	}
	old := runtime.Running.Ident

	fake.StopError = errors.New("still alive after SIGKILL")
	result, err := server.SwitchServer(t.Context(), fake, runtime, e.switchReq(t, "app", "feat-b", spec, false))
	var stopErr *server.StopFailedError
	if !errors.As(err, &stopErr) {
		t.Fatalf("err = %v (%T), want *server.StopFailedError", err, err)
	}
	if runtime.Running == nil || runtime.Running.Ident != old || runtime.Running.Worktree != "feat-a" {
		t.Fatalf("Running must be preserved on stop failure: %+v", runtime.Running)
	}
	if !fake.Alive(old) {
		t.Fatal("old instance should still be alive")
	}
	if got := fake.Spawns(); len(got) != 1 {
		t.Fatalf("must not double-start: %v", got)
	}
	kinds := eventKinds(result.Events)
	if len(kinds) != 2 || kinds[0] != server.EventStoppingOld || kinds[1] != server.EventStopFailed {
		t.Fatalf("events = %v, want [StoppingOld StopFailed]", kinds)
	}
}

// 即死検出: spawn 直後に消滅したサーバーは StartError（ログ末尾つき）で失敗する。
func TestImmediateDeathReportsStartErrorWithLogTail(t *testing.T) {
	e := newTestEnv(t)
	fake := serverfake.New()
	fake.DieOnSpawn = true
	runtime := &server.Runtime{}
	req := e.switchReq(t, "app", "feat-a", server.Spec{Start: cmdspec.FromString("crash")}, false)

	// フェイクはログを書かないため、「サーバーが吐いたはずのログ」はテストが注入する。
	if err := os.WriteFile(req.Log, []byte("boom: missing dependency\nexited\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := server.SwitchServer(t.Context(), fake, runtime, req)
	var startErr *server.StartError
	if !errors.As(err, &startErr) {
		t.Fatalf("err = %v (%T), want *server.StartError", err, err)
	}
	if len(startErr.LogTail) != 2 || startErr.LogTail[0] != "boom: missing dependency" {
		t.Fatalf("LogTail = %q", startErr.LogTail)
	}
	if runtime.Running != nil {
		t.Fatal("a dead-on-arrival server must not be recorded as running")
	}
}
