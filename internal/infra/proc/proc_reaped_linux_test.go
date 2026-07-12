//go:build linux

package proc_test

import (
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/vimrak-hal/worktree-integrator/internal/infra/proc"
)

// TestGroupReapedTrueForZombie は、setsid で自身のプロセスグループを持つ子を起動し、
// SIGKILL 後に親（このテスト）が回収しない＝ゾンビとして残る状況で GroupReaped が
// true を返すことを確認する。回収前のゾンビは kill(-pgid, 0) には生存として数えられ
// 続けるため、「実行は終わっている」を消滅として扱えることがこの判定の要点。
func TestGroupReapedTrueForZombie(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	// setsid によりリーダー = 新しいプロセスグループ（pgid == pid）。
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	pid := cmd.Process.Pid
	pgid := pid
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	// 生きている間（状態 S/R）は消滅とはみなされない。
	if proc.GroupReaped(pgid) {
		t.Fatal("a live group must not be reported as reaped")
	}

	// SIGKILL するが cmd.Wait() は呼ばない → 親が回収しない限りゾンビ（Z）として残る。
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill: %v", err)
	}

	// ゾンビ状態へ遷移し、GroupReaped が消滅を報告するまで待つ。
	deadline := time.Now().Add(2 * time.Second)
	for {
		if proc.GroupReaped(pgid) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("group of a SIGKILLed-but-unreaped member should be reported as reaped")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestGroupReapedTrueForMissingGroup は、メンバーが 1 つも存在しない pgid に対して
// GroupReaped が true（全消滅）を返すことを確認する。
func TestGroupReapedTrueForMissingGroup(t *testing.T) {
	if !proc.GroupReaped(1 << 30) {
		t.Fatal("a group with no members should be reported as reaped")
	}
}
