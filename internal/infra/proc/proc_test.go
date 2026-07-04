package proc_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/vimrak-hal/worktree-integrator/internal/infra/childio"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/proc"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/wtenv"
)

// TestStartTimeOfSelf は、テスト自身のプロセスの開始時刻が「過去だが遠すぎない」
// 妥当な値として取得できることを確認する（実プロセスでの StartTime 検証）。
func TestStartTimeOfSelf(t *testing.T) {
	st, err := proc.StartTime(os.Getpid())
	if err != nil {
		t.Fatalf("StartTime(self): %v", err)
	}
	now := time.Now()
	if st.After(now.Add(time.Second)) {
		t.Fatalf("start time %v is in the future (now %v)", st, now)
	}
	// go test のプロセスがこれより長生きしていることはない。
	if now.Sub(st) > time.Hour {
		t.Fatalf("start time %v is implausibly old (now %v)", st, now)
	}
}

// TestStartTimeOfSpawnedChild は、起動した実プロセス（sleep）の開始時刻が現在時刻の
// 近傍で観測され、終了後には ErrGone になることを確認する。
func TestStartTimeOfSpawnedChild(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	st, err := proc.StartTime(pid)
	if err != nil {
		t.Fatalf("StartTime(child): %v", err)
	}
	if d := time.Since(st); d < -2*time.Second || d > 10*time.Second {
		t.Fatalf("child start time %v is not near now (delta %v)", st, d)
	}

	// 終了・回収後は ErrGone。
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if _, err := proc.StartTime(pid); !errors.Is(err, proc.ErrGone) {
		t.Fatalf("StartTime(dead child) = %v, want ErrGone", err)
	}
}

// TestStartTimeGoneForBogusPid は、存在し得ない pid に対して ErrGone を返すことを確認する。
func TestStartTimeGoneForBogusPid(t *testing.T) {
	if _, err := proc.StartTime(1 << 28); !errors.Is(err, proc.ErrGone) {
		t.Fatalf("StartTime(bogus) = %v, want ErrGone", err)
	}
}

// TestOfAndSameProcess は、Of で採取した Ident が SameProcess で一致と判定され、
// 開始時刻をずらした Ident（= pid 再利用のシミュレート）は不一致になることを確認する。
func TestOfAndSameProcess(t *testing.T) {
	pid := os.Getpid()
	id, err := proc.Of(pid, pid)
	if err != nil {
		t.Fatalf("Of(self): %v", err)
	}
	if id.Pid != pid || id.Pgid != pid || id.StartUnixMs == 0 {
		t.Fatalf("Ident = %+v", id)
	}
	if !proc.SameProcess(id) {
		t.Fatal("SameProcess(self) should be true")
	}

	// 開始時刻が許容幅（±2s）を超えてずれていれば別プロセス。
	reused := id
	reused.StartUnixMs -= 10_000
	if proc.SameProcess(reused) {
		t.Fatal("SameProcess with a 10s-off start time should be false")
	}

	// 存在しないプロセスは常に不一致。
	gone := proc.Ident{Pid: 1 << 28, Pgid: 1 << 28, StartUnixMs: id.StartUnixMs}
	if proc.SameProcess(gone) {
		t.Fatal("SameProcess(bogus pid) should be false")
	}
}

// TestSameStartTolerance は、±2 秒の許容幅の境界を固定する。
func TestSameStartTolerance(t *testing.T) {
	base := time.Now()
	ms := base.UnixMilli()
	if !proc.SameStart(base, ms) {
		t.Fatal("identical times should match")
	}
	if !proc.SameStart(base.Add(1900*time.Millisecond), ms) {
		t.Fatal("+1.9s should be within tolerance")
	}
	if !proc.SameStart(base.Add(-1900*time.Millisecond), ms) {
		t.Fatal("-1.9s should be within tolerance")
	}
	if proc.SameStart(base.Add(2100*time.Millisecond), ms) {
		t.Fatal("+2.1s should be out of tolerance")
	}
	if proc.SameStart(base.Add(-2100*time.Millisecond), ms) {
		t.Fatal("-2.1s should be out of tolerance")
	}
}

// quiet は標準ストリームを繋がない Streams（stdout/stderr はテスト出力を汚さない）。
func quiet() childio.Streams { return childio.Streams{} }

// TestRunExitCodes は、Run の返り値契約（exit 0 → nil、非 0 → *exec.ExitError）を
// 実プロセスで確認する。
func TestRunExitCodes(t *testing.T) {
	if err := proc.Run(t.Context(), "exit 0", t.TempDir(), nil, quiet()); err != nil {
		t.Fatalf("exit 0: %v", err)
	}
	err := proc.Run(t.Context(), "exit 3", t.TempDir(), nil, quiet())
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("exit 3: err = %v (%T), want *exec.ExitError", err, err)
	}
	if exit.ExitCode() != 3 {
		t.Fatalf("exit code = %d, want 3", exit.ExitCode())
	}
}

// TestRunUsesCwdAndEnv は、コマンドが cwd で実行され、渡した環境変数ペアが
// 子プロセスに見えることを確認する。
func TestRunUsesCwdAndEnv(t *testing.T) {
	dir := t.TempDir()
	env := []wtenv.Pair{{Key: "WT_PROBE", Value: "hello"}}
	if err := proc.Run(t.Context(), `printf '%s' "$WT_PROBE" > out.txt`, dir, env, quiet()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "out.txt"))
	if err != nil {
		t.Fatalf("command did not run in cwd: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("env not passed, got %q", data)
	}
}

// TestRunCanceledContext は、キャンセルされた ctx でコマンドが殺され、呼び出し側が
// ctx.Err() で判別できることを確認する（Run 自体は raw なエラーを返す契約）。
func TestRunCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- proc.Run(ctx, "sleep 30", t.TempDir(), nil, quiet()) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled Run should report an error")
		}
		if ctx.Err() == nil {
			t.Fatal("ctx should be canceled")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

// TestRunStreams は、標準出力が streams に接続されることを確認する。
func TestRunStreams(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "out")
	f, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := proc.Run(t.Context(), "echo captured", t.TempDir(), nil, childio.Streams{Stdout: f, Stderr: f}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	data, _ := os.ReadFile(outPath)
	if string(data) != "captured\n" {
		t.Fatalf("stdout = %q", data)
	}
}

// TestIdentZeroNeverMatches は、開始時刻を採取できなかった Ident（StartUnixMs=0）が
// 生きているプロセスと一致しないことを確認する（SpawnDetached の即死フォールバックの
// 前提: Alive が必ず false になり、即死検出に進む）。
func TestIdentZeroNeverMatches(t *testing.T) {
	pid := os.Getpid()
	if proc.SameProcess(proc.Ident{Pid: pid, Pgid: pid, StartUnixMs: 0}) {
		t.Fatal("an Ident with zero start time must not match a live process")
	}
	// 参考: 実プロセスは存在している（kill 0 は成功する）。
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("kill(self, 0): %v", err)
	}
}
