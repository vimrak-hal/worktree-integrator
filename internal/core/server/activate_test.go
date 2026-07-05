package server_test

import (
	"errors"
	"testing"

	"github.com/vimrak-hal/worktree-integrator/internal/core/server"
	"github.com/vimrak-hal/worktree-integrator/internal/core/server/serverfake"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/proc"
)

// spawn はフェイクで 1 つの「プロセス」を起動し、その Ident を返す。
func spawn(t *testing.T, fake *serverfake.Fake, script string) proc.Ident {
	t.Helper()
	id, err := fake.SpawnDetached(script, "/cwd", nil, "/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// TestStopServerNoRunningEntry は、running が無いときは何もせず空の結果を返すことを確認する。
func TestStopServerNoRunningEntry(t *testing.T) {
	fake := serverfake.New()
	runtime := &server.Runtime{}

	res := server.StopServer(t.Context(), fake, runtime)
	if res.Stopped || res.Failed {
		t.Fatalf("result = %+v, want zero", res)
	}
	if len(res.Events) != 0 {
		t.Fatalf("expected no events, got %v", res.Events)
	}
}

// TestStopServerStopsLiveServer は、生存中のサーバーを止め、消滅確認の後に
// EventStopped を発行し、running をクリアすることを確認する。
func TestStopServerStopsLiveServer(t *testing.T) {
	fake := serverfake.New()
	id := spawn(t, fake, "run")
	runtime := &server.Runtime{Running: &server.Instance{Ident: id, Worktree: "feat-a", GraceSecs: 5}}

	res := server.StopServer(t.Context(), fake, runtime)
	if !res.Stopped || res.Failed {
		t.Fatalf("result = %+v", res)
	}
	if runtime.Running != nil {
		t.Fatal("running entry should be cleared")
	}
	if len(res.Events) != 1 || res.Events[0].Kind != server.EventStopped {
		t.Fatalf("events = %+v, want a single EventStopped", res.Events)
	}
	if res.Events[0].Pid != id.Pid {
		t.Fatalf("EventStopped Pid = %d, want %d", res.Events[0].Pid, id.Pid)
	}
	if fake.Alive(id) {
		t.Fatal("group should be stopped after StopServer")
	}
}

// TestStopServerFailureKeepsRunning は、StopGroup が失敗したとき Running を保持した
// まま EventStopFailed を発行することを確認する（孤児を台帳から消さない）。
func TestStopServerFailureKeepsRunning(t *testing.T) {
	sentinel := errors.New("kill failed")
	fake := serverfake.New()
	fake.StopError = sentinel
	id := spawn(t, fake, "run")
	runtime := &server.Runtime{Running: &server.Instance{Ident: id, Worktree: "feat-a"}}

	res := server.StopServer(t.Context(), fake, runtime)
	if res.Stopped {
		t.Fatal("Stopped must be false on failure")
	}
	if !res.Failed {
		t.Fatal("Failed should be true")
	}
	if runtime.Running == nil || runtime.Running.Ident != id {
		t.Fatalf("Running must be preserved on stop failure: %+v", runtime.Running)
	}
	if len(res.Events) != 1 || res.Events[0].Kind != server.EventStopFailed {
		t.Fatalf("events = %+v, want a single EventStopFailed", res.Events)
	}
	if !errors.Is(res.Events[0].Err, sentinel) {
		t.Fatalf("EventStopFailed.Err = %v, want %v", res.Events[0].Err, sentinel)
	}
	if !fake.Alive(id) {
		t.Fatal("the process should still be alive")
	}
}

// TestStopServerAlreadyDead は、記録されたサーバーが既に死亡しているとき
// EventAlreadyStopped を発行し、シグナルを送らないことを確認する。
func TestStopServerAlreadyDead(t *testing.T) {
	fake := serverfake.New() // 起動していないので、どの Ident も生存していない
	runtime := &server.Runtime{Running: &server.Instance{Ident: proc.Ident{Pid: 7, Pgid: 7, StartUnixMs: 1}}}

	res := server.StopServer(t.Context(), fake, runtime)
	if !res.Stopped {
		t.Fatal("Stopped should be true (there was a recorded entry)")
	}
	if runtime.Running != nil {
		t.Fatal("running entry should be cleared")
	}
	if len(res.Events) != 1 || res.Events[0].Kind != server.EventAlreadyStopped {
		t.Fatalf("events = %+v, want a single EventAlreadyStopped", res.Events)
	}
	if len(fake.StopSignals()) != 0 {
		t.Fatalf("no signal should be sent to a dead entry: %v", fake.StopSignals())
	}
}

// TestStopServerPgidReuseDoesNotKill は、pgid 再利用誤殺の防止を固定する: 記録された
// Ident と同じ pid/pgid を持つが開始時刻の異なる（= 番号を再利用した無関係の）
// プロセスが生きていても、StopGroup はシグナルを送らず、記録は「既に停止」として
// 整理される。
func TestStopServerPgidReuseDoesNotKill(t *testing.T) {
	fake := serverfake.New()
	// 現在生きているプロセス（＝再利用側）。
	current := spawn(t, fake, "innocent")
	// 台帳の記録は同じ番号だが別の開始時刻（＝過去に死んだ我々のサーバー）。
	stale := current
	stale.StartUnixMs -= 10_000
	runtime := &server.Runtime{Running: &server.Instance{Ident: stale, Worktree: "feat-a"}}

	res := server.StopServer(t.Context(), fake, runtime)
	if !res.Stopped {
		t.Fatal("the stale record should be cleared")
	}
	if len(res.Events) != 1 || res.Events[0].Kind != server.EventAlreadyStopped {
		t.Fatalf("events = %+v, want EventAlreadyStopped", res.Events)
	}
	if len(fake.StopSignals()) != 0 {
		t.Fatalf("SameProcess 不一致でシグナルを送ってはならない: %v", fake.StopSignals())
	}
	if !fake.Alive(current) {
		t.Fatal("the innocent current process must survive")
	}
}

// TestStopGroupDirectPgidReuse は、フェイクの StopGroup 契約そのものを固定する:
// 同一性不一致の Ident には何も送らず nil を返す。
func TestStopGroupDirectPgidReuse(t *testing.T) {
	fake := serverfake.New()
	current := spawn(t, fake, "innocent")
	stale := current
	stale.StartUnixMs -= 10_000

	if err := fake.StopGroup(t.Context(), stale, 0); err != nil {
		t.Fatalf("StopGroup(stale) = %v, want nil (already gone)", err)
	}
	if !fake.Alive(current) {
		t.Fatal("current process must not be killed via a stale ident")
	}
	if len(fake.StopSignals()) != 0 {
		t.Fatalf("signals = %v, want none", fake.StopSignals())
	}
}

// TestProbeStopped は、running が無いときの Probe を確認する。
func TestProbeStopped(t *testing.T) {
	fake := serverfake.New()
	runtime := &server.Runtime{}

	state, pid, changed := server.Probe(t.Context(), fake, runtime)
	if state != server.StatusStopped || pid != 0 || changed {
		t.Fatalf("Probe(stopped) = (%v, %d, %v)", state, pid, changed)
	}
}

// TestProbeRunning は、生存中のインスタンスに対する Probe を確認する。
func TestProbeRunning(t *testing.T) {
	fake := serverfake.New()
	id := spawn(t, fake, "run")
	runtime := &server.Runtime{Running: &server.Instance{Ident: id, Worktree: "feat-a"}}

	state, pid, changed := server.Probe(t.Context(), fake, runtime)
	if state != server.StatusRunning {
		t.Fatalf("state = %v, want StatusRunning", state)
	}
	if pid != id.Pid {
		t.Fatalf("pid = %d, want %d", pid, id.Pid)
	}
	if changed {
		t.Fatal("a running server should not be reported as changed")
	}
	if runtime.Running == nil {
		t.Fatal("running entry should be preserved for a live server")
	}
}

// TestProbeSelfHealsCrashed は、消滅したインスタンスの記録が Probe で整理されることを確認する。
func TestProbeSelfHealsCrashed(t *testing.T) {
	fake := serverfake.New()
	runtime := &server.Runtime{
		Running: &server.Instance{Ident: proc.Ident{Pid: 5, Pgid: 5, StartUnixMs: 1}, Worktree: "feat-a"},
	}
	state, _, changed := server.Probe(t.Context(), fake, runtime)
	if state != server.StatusCrashed || !changed || runtime.Running != nil {
		t.Fatalf("state=%v changed=%v running=%v", state, changed, runtime.Running)
	}
}

// TestProbeTreatsIdentityMismatchAsCrashed は、pgid が別プロセスに再利用されていても
// （グループとしては生存）Probe が同一性不一致からクラッシュ扱いで自己修復することを
// 確認する。
func TestProbeTreatsIdentityMismatchAsCrashed(t *testing.T) {
	fake := serverfake.New()
	current := spawn(t, fake, "innocent")
	stale := current
	stale.StartUnixMs -= 10_000
	runtime := &server.Runtime{Running: &server.Instance{Ident: stale, Worktree: "feat-a"}}

	state, _, changed := server.Probe(t.Context(), fake, runtime)
	if state != server.StatusCrashed || !changed || runtime.Running != nil {
		t.Fatalf("state=%v changed=%v running=%v", state, changed, runtime.Running)
	}
	if !fake.Alive(current) {
		t.Fatal("probe must not touch the innocent current process")
	}
}

// TestStepErrorErrorAndUnwrap は、StepError の Error() と Unwrap() を確認する。
func TestStepErrorErrorAndUnwrap(t *testing.T) {
	cause := errors.New("disk full")
	withCause := &server.StepError{Step: "setup", Cause: cause}
	if got := withCause.Error(); got != "setup: disk full" {
		t.Fatalf("Error() = %q", got)
	}
	if !errors.Is(withCause, cause) {
		t.Fatal("Unwrap should expose the cause")
	}

	// Cause が nil の場合（コマンドが 0 以外で終了した場合）。
	exitNonZero := &server.StepError{Step: "on_switch"}
	if got := exitNonZero.Error(); got != "on_switch exited non-zero" {
		t.Fatalf("Error() = %q", got)
	}
	if exitNonZero.Unwrap() != nil {
		t.Fatal("Unwrap should be nil when there is no cause")
	}
}

// TestStartErrorErrorAndUnwrap は、StartError の Error() と Unwrap() を確認する。
func TestStartErrorErrorAndUnwrap(t *testing.T) {
	cause := errors.New("no such file")
	e := &server.StartError{Cause: cause, LogTail: []string{"line"}}
	if got := e.Error(); got != "start: no such file" {
		t.Fatalf("Error() = %q", got)
	}
	if !errors.Is(e, cause) {
		t.Fatal("Unwrap should expose the cause")
	}
}

// TestStopFailedErrorErrorAndUnwrap は、StopFailedError の Error() と Unwrap() を確認する。
func TestStopFailedErrorErrorAndUnwrap(t *testing.T) {
	e := &server.StopFailedError{Cause: server.ErrStillRunning}
	if got := e.Error(); got != "stop previous instance: process group still alive after SIGKILL" {
		t.Fatalf("Error() = %q", got)
	}
	if !errors.Is(e, server.ErrStillRunning) {
		t.Fatal("Unwrap should expose ErrStillRunning")
	}
}
