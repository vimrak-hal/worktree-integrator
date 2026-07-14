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
	for !proc.GroupReaped(pgid) {
		if time.Now().After(deadline) {
			t.Fatal("group of a SIGKILLed-but-unreaped member should be reported as reaped")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestGroupReapedFalseWhenNoMemberObserved は、メンバーを 1 つも観測できない pgid に
// 対して GroupReaped が false（消滅と断定しない）を返すことを確認する。GroupReaped は
// kill(-pgid, 0) がグループの存在を示した文脈でのみ呼ばれるため、観測ゼロは「全滅」では
// なく「hidepid 等で観測不能」と解釈するのが保守的で安全である（真の全滅なら kill が
// ESRCH を返し、この関数は呼ばれない）。
func TestGroupReapedFalseWhenNoMemberObserved(t *testing.T) {
	if proc.GroupReaped(1 << 30) {
		t.Fatal("a group with no observable members must not be reported as reaped")
	}
}
