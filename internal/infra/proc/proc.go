// Package proc は、プロセスの同一性トークン（Ident）と、`sh -c` による共通の
// コマンド実行（Run）を提供する純機構パッケージである。
//
// PID（およびプロセスグループ ID）はカーネルによって再利用されるため、「その PID が
// 生きている」ことは「あのとき起動したプロセスが生きている」ことを意味しない。
// Ident は PID とその OS 報告の開始時刻の組で同一性を担保し、シグナル送出前の検証
// （SameProcess）によって、pgid 再利用による無関係プロセスの誤殺を防ぐ。
//
// 開始時刻の取得（StartTime）は OS ごとにビルドタグで分岐する:
//   - darwin: sysctl kern.proc.pid → kinfo_proc.p_starttime（proc_darwin.go）
//   - linux:  /proc/<pid>/stat の starttime × USER_HZ + /proc/stat の btime（proc_linux.go）
package proc

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"time"

	"github.com/vimrak-hal/worktree-integrator/internal/infra/childio"
)

// ErrGone は、問い合わせた PID のプロセスが存在しないことを表す。呼び出し側は
// errors.Is で「取得の失敗」と「プロセスの消滅」を区別できる。
var ErrGone = errors.New("process not found")

// startTolerance は開始時刻照合の許容幅。OS のクロック粒度（linux の starttime は
// USER_HZ=100 の 10ms 粒度、btime は秒粒度）と記録・観測間の丸め差を吸収する。
// PID の再利用が同一 PID・±2 秒以内の開始時刻で起きる確率は実用上無視できる。
const startTolerance = 2 * time.Second

// Ident はプロセスの同一性トークン。PID・プロセスグループ ID と、その PID の
// OS 報告開始時刻（エポックミリ秒）の組。開始時刻が一致しない PID は「同じ番号を
// 再利用した別のプロセス」であり、シグナルを送ってはならない。
type Ident struct {
	Pid         int   `toml:"pid"`
	Pgid        int   `toml:"pgid"`
	StartUnixMs int64 `toml:"start_unix_ms"`
}

// Of は現在生きているプロセス pid の Ident を採取する。プロセスが既に存在しない
// 場合は ErrGone を返す。
func Of(pid, pgid int) (Ident, error) {
	st, err := StartTime(pid)
	if err != nil {
		return Ident{}, err
	}
	return Ident{Pid: pid, Pgid: pgid, StartUnixMs: st.UnixMilli()}, nil
}

// SameProcess は id.Pid が今も、id を採取したときと同一のプロセスかを開始時刻の
// 照合（±2 秒許容）で検証する。プロセスが存在しない、または開始時刻を取得できない
// 場合は false（同一性を確認できないものにシグナルを送らせない）。
func SameProcess(id Ident) bool {
	st, err := StartTime(id.Pid)
	if err != nil {
		return false
	}
	return SameStart(st, id.StartUnixMs)
}

// SameStart は観測した開始時刻 st が、記録された開始時刻（エポックミリ秒）と
// 許容幅（±2 秒）内で一致するかを返す。
func SameStart(st time.Time, recordedUnixMs int64) bool {
	diff := st.Sub(time.UnixMilli(recordedUnixMs))
	if diff < 0 {
		diff = -diff
	}
	return diff <= startTolerance
}

// processExists は pid のプロセスが存在するかを kill(pid, 0) で確認する。EPERM は
// 「存在するが所有者が異なる」ことを意味するため、存在とみなす。
func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// waitDelay は、コマンドの終了（またはキャンセルによる kill）後も子孫プロセスが
// 標準出力/エラーのパイプを握り続けている場合に、Wait のパイプ待ちを打ち切るまでの
// 猶予（core/git と同じ理由）。
const waitDelay = 2 * time.Second

// Run は script を `sh -c` として cwd で完了まで実行する。フック（core/hooks）と
// サーバーのライフサイクルコマンド（core/server）が共有する唯一のフォアグラウンド
// 実行経路であり、WaitDelay・標準ストリームの接続をここに一本化する。
// cwd が空の場合は呼び出し元のカレントディレクトリを引き継ぐ。
//
// env は子プロセスの環境を "KEY=VALUE" 文字列のスライス（os/exec の Cmd.Env と
// 同形）で与える。nil を渡すと呼び出し元プロセスの環境をそのまま継承する。
// 継承環境への WT_* 変数の合成は呼び出し側の責務であり、この純機構は与えられた
// env をそのまま子プロセスへ渡す。
//
// 返り値の契約: 正常終了（exit 0）なら nil。実行はされたが 0 以外で終了した場合は
// *exec.ExitError（呼び出し側が errors.As で判別する）。それ以外は実行自体の失敗。
// ctx のキャンセルで殺されたコマンドも *exec.ExitError（"signal: killed"）を報告する
// ため、キャンセルを区別したい呼び出し側は ctx.Err() を先に確認すること。
func Run(ctx context.Context, script, cwd string, env []string, streams childio.Streams) error {
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.WaitDelay = waitDelay
	cmd.Dir = cwd
	cmd.Env = env
	cmd.Stdin = streams.Stdin
	cmd.Stdout = streams.Stdout
	cmd.Stderr = streams.Stderr
	return cmd.Run()
}
