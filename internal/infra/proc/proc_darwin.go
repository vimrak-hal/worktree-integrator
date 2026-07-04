//go:build darwin

package proc

import (
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// StartTime は pid の開始時刻を sysctl kern.proc.pid（kinfo_proc.p_starttime）から
// 読み取る。プロセスが存在しない場合は ErrGone を返す。
func StartTime(pid int) (time.Time, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		// 存在しない pid への問い合わせに macOS は「エラーなしの空応答」を返すことが
		// あり、x/sys はそれを EIO などに写す。エラーの種別ではなく kill(pid, 0) の
		// 実在確認で「取得の失敗」と「プロセスの消滅」を判別し、後者を ErrGone に
		// 正規化する。
		if !processExists(pid) {
			return time.Time{}, fmt.Errorf("pid %d: %w", pid, ErrGone)
		}
		return time.Time{}, fmt.Errorf("sysctl kern.proc.pid %d: %w", pid, err)
	}
	tv := kp.Proc.P_starttime
	return time.Unix(tv.Sec, int64(tv.Usec)*int64(time.Microsecond)), nil
}
