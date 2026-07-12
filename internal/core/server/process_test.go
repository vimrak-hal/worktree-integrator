package server_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vimrak-hal/worktree-integrator/internal/core/server"
	"github.com/vimrak-hal/worktree-integrator/internal/core/wtenv"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/childio"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/proc"
)

// quietProc は、テスト中に標準出力を汚さない UnixProcess を返す。
func quietProc() *server.UnixProcess {
	return server.NewUnixProcess(childio.Quiet())
}

// TestRunForegroundExitCodes は、実プロセスでの成功（exit 0）と 0 以外の終了（exit 1）を確認する。
func TestRunForegroundExitCodes(t *testing.T) {
	p := quietProc()
	dir := t.TempDir()

	ok, err := p.RunForeground(t.Context(), "exit 0", dir, nil)
	if err != nil {
		t.Fatalf("exit 0: unexpected error %v", err)
	}
	if !ok {
		t.Fatal("exit 0 should report ok=true")
	}

	ok, err = p.RunForeground(t.Context(), "exit 1", dir, nil)
	if err != nil {
		t.Fatalf("exit 1: a non-zero exit is not an error, got %v", err)
	}
	if ok {
		t.Fatal("exit 1 should report ok=false")
	}
}

// TestRunForegroundRunsInCwdWithEnv は、コマンドが cwd で実行され WT_* 環境変数が
// 渡されることを、ファイルへの書き出しで確認する。
func TestRunForegroundRunsInCwdWithEnv(t *testing.T) {
	p := quietProc()
	dir := t.TempDir()
	env := []wtenv.Pair{{Key: "WT_PROBE", Value: "hello"}}

	// cwd 内の out.txt に環境変数を書き出す。
	ok, err := p.RunForeground(t.Context(), `printf '%s' "$WT_PROBE" > out.txt`, dir, env)
	if err != nil || !ok {
		t.Fatalf("RunForeground ok=%v err=%v", ok, err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "out.txt"))
	if err != nil {
		t.Fatalf("command did not run in cwd: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("WT_PROBE not passed through env, got %q", data)
	}
}

// TestSpawnDetachedAndStopGroup は、デタッチして起動したサーバーが Ident つきで生存し、
// その出力がログへ書かれ、StopGroup でグループが終了することを実プロセスで確認する。
func TestSpawnDetachedAndStopGroup(t *testing.T) {
	p := quietProc()
	dir := t.TempDir()
	log := filepath.Join(dir, "server.log")

	// 起動直後に印を書き、その後しばらく生存するサーバー。
	id, err := p.SpawnDetached(`echo started; sleep 30`, dir, nil, log)
	if err != nil {
		t.Fatalf("SpawnDetached: %v", err)
	}
	// 万一の取りこぼしに備え、テスト終了時に必ず後始末する。
	t.Cleanup(func() { _ = p.StopGroup(context.Background(), id, time.Second) })

	if id.Pid <= 0 || id.Pgid != id.Pid {
		t.Fatalf("ident = %+v, want pgid==pid>0", id)
	}
	// 同一性トークンには実プロセスの開始時刻が採取されている。
	if id.StartUnixMs == 0 {
		t.Fatalf("ident should carry the start time: %+v", id)
	}
	if d := time.Since(time.UnixMilli(id.StartUnixMs)); d < -2*time.Second || d > 10*time.Second {
		t.Fatalf("start time not near now (delta %v)", d)
	}

	if !p.Alive(id) {
		t.Fatal("spawned group should be alive")
	}

	// ログにサーバーの出力が現れるのを待つ。
	waitFor(t, time.Second, func() bool {
		data, _ := os.ReadFile(log)
		return strings.Contains(string(data), "started")
	})

	// 停止する: SIGTERM で素直に終わるはず（短い猶予で十分）。
	if err := p.StopGroup(t.Context(), id, 500*time.Millisecond); err != nil {
		t.Fatalf("StopGroup: %v", err)
	}
	if p.Alive(id) {
		t.Fatal("group should be dead after StopGroup")
	}
}

// TestSpawnDetachedCreatesLogDir は、ログディレクトリの作成が spawn 経路で行われる
// ことを確認する（読むだけの経路ではディレクトリが生えない、の裏面）。
func TestSpawnDetachedCreatesLogDir(t *testing.T) {
	p := quietProc()
	logsDir := filepath.Join(t.TempDir(), "logs")
	log := filepath.Join(logsDir, "app__backend__feat-a.log")

	id, err := p.SpawnDetached("true", t.TempDir(), nil, log)
	if err != nil {
		t.Fatalf("SpawnDetached: %v", err)
	}
	t.Cleanup(func() { _ = p.StopGroup(context.Background(), id, time.Second) })
	info, err := os.Stat(logsDir)
	if err != nil || !info.IsDir() {
		t.Fatalf("logs dir should be created on the spawn path: %v", err)
	}
}

// TestSpawnDetachedRotatesPreviousLog は、SpawnDetached が既存のログを .prev へ
// ローテートし、現在のログが現行インスタンスの出力だけになることを確認する。
func TestSpawnDetachedRotatesPreviousLog(t *testing.T) {
	p := quietProc()
	dir := t.TempDir()
	log := filepath.Join(dir, "server.log")
	if err := os.WriteFile(log, []byte("old generation\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	id, err := p.SpawnDetached(`echo new generation`, dir, nil, log)
	if err != nil {
		t.Fatalf("SpawnDetached: %v", err)
	}
	t.Cleanup(func() { _ = p.StopGroup(context.Background(), id, time.Second) })

	prev, err := os.ReadFile(server.PrevLogPath(log))
	if err != nil {
		t.Fatalf("previous generation should be rotated aside: %v", err)
	}
	if string(prev) != "old generation\n" {
		t.Fatalf(".prev = %q", prev)
	}
	waitFor(t, 2*time.Second, func() bool {
		data, _ := os.ReadFile(log)
		return strings.Contains(string(data), "new generation")
	})
	data, _ := os.ReadFile(log)
	if strings.Contains(string(data), "old generation") {
		t.Fatalf("current log must contain only the current instance's output: %q", data)
	}
}

// TestStopGroupSigkillEscalation は、SIGTERM を無視するプロセスに対して、猶予経過後に
// SIGKILL へエスカレートしてグループを終了させることを確認する。
func TestStopGroupSigkillEscalation(t *testing.T) {
	p := quietProc()
	dir := t.TempDir()
	log := filepath.Join(dir, "server.log")

	// SIGTERM を捕捉して無視し、印を書いてから生存し続けるサーバー。
	script := `trap '' TERM; echo trapped; sleep 30`
	id, err := p.SpawnDetached(script, dir, nil, log)
	if err != nil {
		t.Fatalf("SpawnDetached: %v", err)
	}
	t.Cleanup(func() { _ = p.StopGroup(context.Background(), id, time.Second) })

	// trap が確立されるまで待ってから停止を要求する。
	waitFor(t, 2*time.Second, func() bool {
		data, _ := os.ReadFile(log)
		return strings.Contains(string(data), "trapped")
	})

	// 短い猶予を渡す: SIGTERM は無視されるので SIGKILL へエスカレートするはず。
	start := time.Now()
	if err := p.StopGroup(t.Context(), id, 200*time.Millisecond); err != nil {
		t.Fatalf("StopGroup: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Fatalf("StopGroup returned too quickly (%v); expected to wait out the grace before SIGKILL", elapsed)
	}
	if p.Alive(id) {
		t.Fatal("group should be killed after grace + SIGKILL")
	}
}

// TestAliveIdentityMismatchAndStopGroupDoesNotKill は、実プロセスでの pgid 再利用
// 誤殺防止を固定する: 生きているプロセスと同じ番号でも開始時刻の異なる Ident は
// Alive=false であり、StopGroup はシグナルを送らずに nil を返し、そのプロセスは
// 生き残る。
func TestAliveIdentityMismatchAndStopGroupDoesNotKill(t *testing.T) {
	p := quietProc()
	dir := t.TempDir()
	log := filepath.Join(dir, "server.log")

	id, err := p.SpawnDetached("sleep 30", dir, nil, log)
	if err != nil {
		t.Fatalf("SpawnDetached: %v", err)
	}
	t.Cleanup(func() { _ = p.StopGroup(context.Background(), id, time.Second) })

	// 「10 秒前に開始したはずの同じ番号のプロセス」= 再利用をシミュレートした記録。
	stale := id
	stale.StartUnixMs -= 10_000

	if p.Alive(stale) {
		t.Fatal("an ident with a mismatched start time must not be alive")
	}
	if err := p.StopGroup(t.Context(), stale, 100*time.Millisecond); err != nil {
		t.Fatalf("StopGroup(stale) = %v, want nil", err)
	}
	// 本物のプロセスは無傷。
	if !p.Alive(id) {
		t.Fatal("the real process must survive a stop via a stale ident")
	}
}

// TestAliveLeaderGoneButGroupSurvives は、リーダー（sh）が終了してもグループに
// 子孫（sleep）が残っている場合に Alive が true を維持し、StopGroup がその子孫を
// 終了できることを確認する（使用中の pgid は再割り当てされないという POSIX の
// 保証に基づく分岐）。
func TestAliveLeaderGoneButGroupSurvives(t *testing.T) {
	p := quietProc()
	dir := t.TempDir()
	log := filepath.Join(dir, "server.log")

	// sh はバックグラウンドの sleep を残して即座に終了する。sleep は同じ
	// プロセスグループに残る。
	id, err := p.SpawnDetached(`sleep 30 & echo forked`, dir, nil, log)
	if err != nil {
		t.Fatalf("SpawnDetached: %v", err)
	}
	t.Cleanup(func() { _ = p.StopGroup(context.Background(), id, time.Second) })

	// リーダーの消滅を待つ（グループはまだ生きている）。
	waitFor(t, 2*time.Second, func() bool {
		_, err := proc.StartTime(id.Pid)
		return err != nil
	})

	if !p.Alive(id) {
		t.Fatal("group with surviving descendants should still be alive")
	}
	if err := p.StopGroup(t.Context(), id, 500*time.Millisecond); err != nil {
		t.Fatalf("StopGroup: %v", err)
	}
	if p.Alive(id) {
		t.Fatal("descendants should be stopped")
	}
}

// TestAliveOnDeadGroup は、存在しないグループに対して Alive が false を返すことを確認する。
func TestAliveOnDeadGroup(t *testing.T) {
	p := quietProc()
	// 採番されないであろう非常に大きな pgid。生存していてはならない。
	if p.Alive(proc.Ident{Pid: 1 << 30, Pgid: 1 << 30, StartUnixMs: 1}) {
		t.Fatal("a non-existent group should not be alive")
	}
}

// TestStopGroupOnDeadGroupIsNoError は、既に消滅したグループへの StopGroup がエラーなく返ることを確認する。
func TestStopGroupOnDeadGroupIsNoError(t *testing.T) {
	p := quietProc()
	dead := proc.Ident{Pid: 1 << 30, Pgid: 1 << 30, StartUnixMs: 1}
	if err := p.StopGroup(t.Context(), dead, 100*time.Millisecond); err != nil {
		t.Fatalf("StopGroup on a dead group should not error, got %v", err)
	}
}

// waitFor は、cond が真になるまで（最大 timeout）ポーリングし、間に合わなければ失敗する。
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}
