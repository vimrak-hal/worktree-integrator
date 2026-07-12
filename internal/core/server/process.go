package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/vimrak-hal/worktree-integrator/internal/infra/childio"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/proc"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/wtenv"
)

// pollInterval は、停止したグループの終了を待つ間にポーリングする間隔。
const pollInterval = 50 * time.Millisecond

// ErrStillRunning は、SIGKILL 後もプロセスグループの消滅を確認できなかったことを
// 表す。StopGroup は消滅を確認できた場合のみ nil を返す。この失敗を受け取った
// 状態機械は Running の記録を保持する（孤児を台帳から消さない。次回コマンドで
// 再試行できる）。
var ErrStillRunning = errors.New("process group still alive after SIGKILL")

// ProcessControl は、server サブシステムが必要とする OS のプロセス操作を抽象化し、
// activate ロジックをフェイクでテスト可能にする。生存確認と停止は pgid の数値では
// なく proc.Ident（PID + 開始時刻）を受け取り、実装はシグナル送出前に必ず同一性を
// 検証する。番号の再利用による無関係プロセスの誤殺を、インターフェイスの形で防ぐ。
type ProcessControl interface {
	// RunForeground は短命なコマンドを cwd で env を用いて完了まで実行し、正常に
	// 終了したかどうかを返す。エラーはそもそも実行できなかった、または ctx で
	// キャンセルされたことを意味する（0 以外の終了は ok=false として報告され、
	// エラーにはならない）。
	RunForeground(ctx context.Context, script, cwd string, env []wtenv.Pair) (bool, error)
	// SpawnDetached は script を、自身のセッション／プロセスグループにデタッチした
	// サーバーとして起動し、stdout と stderr を logPath へ書き込む。logPath に既存の
	// ログがあれば <logPath>.prev へローテートし（1 世代保持）、「現在のログ =
	// 現行インスタンスの出力」という不変条件を保つ。
	// デタッチして呼び出し元より長く生きることが仕様であるため、意図的に ctx の
	// 対象外である（キャンセルで起動済みサーバーを殺してはならない）。
	SpawnDetached(script, cwd string, env []wtenv.Pair, logPath string) (proc.Ident, error)
	// Alive は、id の指すプロセスグループがまだ生存しているかどうかを報告する。
	// グループの生存（kill(-pgid, 0)）に加えてリーダープロセスの同一性
	// （proc.SameProcess）を検証し、番号が再利用されただけの無関係なグループを
	// 生存と誤認しない。
	Alive(id proc.Ident) bool
	// StopGroup は id のプロセスグループを停止する: SIGTERM を送り、grace 経過後も
	// まだ生存していれば SIGKILL を送る。シグナル送出前に必ず同一性を検証し、
	// 一致しない場合は何も送らずに nil を返す（対象は既に消滅している）。
	// 消滅を確認できた場合のみ nil を返し、SIGKILL 後も生存している場合は
	// ErrStillRunning を返す。ctx のキャンセルは grace の待機を打ち切って即座に
	// SIGKILL へエスカレートする（シグナル送出済みのグループを放置しないため）。
	StopGroup(ctx context.Context, id proc.Ident, grace time.Duration) error
}

// UnixProcess は実際の ProcessControl。長時間稼働するサーバーを自身のセッションに
// デタッチして起動し（そのため CLI の終了やターミナルのクローズ後も生き残る）、停止時には
// プロセスグループ全体にシグナルを送る。
type UnixProcess struct {
	io childio.Streams
}

// NewUnixProcess は、フォアグラウンドのライフサイクルコマンドが標準ストリームに io を
// 使う UnixProcess を構築する。
func NewUnixProcess(io childio.Streams) *UnixProcess {
	return &UnixProcess{io: io}
}

// groupAlive は、シグナル 0 でグループにシグナルを送ったときに、それがまだ存在することを
// 示すかどうかを報告する。EPERM はグループが存在するが別のユーザーが所有していることを
// 意味し、これも生存とみなす。
func groupAlive(pgid int) bool {
	err := syscall.Kill(-pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// RunForeground はライフサイクルコマンドを完了まで実行する。実行そのものは proc.Run
// （フックと共通の sh -c 実行経路）に委ね、ここでは結果を server の契約（ok, err）へ
// 写すだけを担う。
func (u *UnixProcess) RunForeground(ctx context.Context, script, cwd string, env []wtenv.Pair) (bool, error) {
	err := proc.Run(ctx, script, cwd, env, u.io)
	if err == nil {
		return true, nil
	}
	// キャンセルで殺されたコマンドは *exec.ExitError（"signal: killed"）を報告
	// するため、「実行はされたが 0 以外で終了した」に紛れないよう ctx を先に見る。
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, fmt.Errorf("run command `%s`: %w", script, ctxErr)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return false, nil // 実行はされたが、0 以外で終了した
	}
	return false, fmt.Errorf("run command `%s`: %w", script, err)
}

// SpawnDetached は長時間稼働するサーバーを自身のセッションで起動する。
// インターフェイスのコメントのとおり、デタッチが仕様のため ctx は取らない。
// ログディレクトリの作成と既存ログの .prev へのローテートもここ（実際にログを
// 書く直前）で行う。状態ストアのロック取得が logs/ を作っていた頃の「status を
// 見るだけでディレクトリが生える」副作用を持ち込まないため、作成はこの起動経路のみ。
func (u *UnixProcess) SpawnDetached(script, cwd string, env []wtenv.Pair, logPath string) (proc.Ident, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return proc.Ident{}, fmt.Errorf("create log directory %s: %w", filepath.Dir(logPath), err)
	}
	// 既存ログを 1 世代だけ保持する。これから書くログが現行インスタンスの出力だけに
	// なるため、即死検出（LogTail）が前のインスタンスの出力を混ぜて報告しない。
	if _, err := os.Stat(logPath); err == nil {
		if err := os.Rename(logPath, logPath+".prev"); err != nil {
			return proc.Ident{}, fmt.Errorf("rotate log file %s: %w", logPath, err)
		}
	}
	out, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return proc.Ident{}, fmt.Errorf("open log file %s: %w", logPath, err)
	}
	defer out.Close()

	cmd := exec.Command("sh", "-c", script)
	cmd.Dir = cwd
	cmd.Env = wtenv.Environ(os.Environ(), env)
	cmd.Stdin = nil // /dev/null
	cmd.Stdout = out
	cmd.Stderr = out
	// 新しいセッションを開始する: これにより子プロセスを制御端末から切り離し
	//（ターミナルのクローズ後も生き残る）、プロセスグループのリーダーにする。そのため
	// pgid == pid となり、後でグループ全体にシグナルを送れる。
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return proc.Ident{}, fmt.Errorf("spawn server `%s`: %w", script, err)
	}
	pid := cmd.Process.Pid
	// 最終的に終了したとき（停止によるかクラッシュによるかを問わず）に非同期で回収し、
	// 長命な MCP サーバーがゾンビを蓄積しないようにする。
	go func() { _ = cmd.Wait() }()

	// 同一性トークンを起動直後に採取する。子が即死して開始時刻を取れなかった場合も
	// 起動自体は成功として報告し、開始時刻ゼロの Ident（Alive が必ず false）を返す。
	// 即死は呼び出し元の起動監視（watchStartup）がログ末尾とともに報告する。
	id, err := proc.Of(pid, pid)
	if err != nil {
		return proc.Ident{Pid: pid, Pgid: pid}, nil
	}
	return id, nil
}

// Alive は、id の指すプロセスグループがまだ生存しているかどうかを報告する。
func (u *UnixProcess) Alive(id proc.Ident) bool {
	if !groupAlive(id.Pgid) {
		return false
	}
	// kill(-pgid, 0) が生存を示しても、メンバーがすべてゾンビ（SIGKILL 済みだが
	// 未回収）なら実行は終わっている。PID 1 が最小 init のコンテナ環境では孤児が
	// 回収されず、この判定が無いとグループは永遠に生存扱いになる（誤殺は Ident の
	// 同一性照合で防がれるため、消滅扱いにしてもリスクは増えない）。
	if proc.GroupReaped(id.Pgid) {
		return false
	}
	st, err := proc.StartTime(id.Pid)
	switch {
	case errors.Is(err, proc.ErrGone):
		// リーダー（pid == pgid）は終了したがグループは存続している。使用中の pgid は
		// 新しいプロセスに再割り当てされない（POSIX）ため、生き残っているメンバーは
		// 我々が起動したグループの子孫であり、生存とみなす。
		return true
	case err != nil:
		// 同一性を確認できないものは死んでいる扱いにする（誤殺の回避を優先する）。
		return false
	default:
		return proc.SameStart(st, id.StartUnixMs)
	}
}

// StopGroup はプロセスグループを停止し、grace 経過後に SIGKILL へエスカレートする。
// ctx がキャンセルされた場合は grace を待たずに即座に SIGKILL へ進む。SIGTERM を
// 送った後にグループを放置して戻ると停止途中の状態が残るため、キャンセルへの応答は
// 「早く戻る」ではなく「早くエスカレートする」で行う。
//
// 生存確認は常に Alive（同一性検証込み）で行う。SIGTERM の後にグループが消滅して
// 番号が再利用された場合でも、無関係な新プロセスへ SIGKILL を送ることはない。
func (u *UnixProcess) StopGroup(ctx context.Context, id proc.Ident, grace time.Duration) error {
	// シグナル送出前の同一性検証。一致しなければ対象は既に消滅している
	//（あるいは番号が別のプロセスに再利用されている）ので、何も送らずに成功とする。
	if !u.Alive(id) {
		return nil
	}

	// グループ全体に丁寧に終了を要求する。シグナルの送信に失敗し、かつグループが
	// すでに消滅している場合は、何もすることはない。
	if err := syscall.Kill(-id.Pgid, syscall.SIGTERM); err != nil && !u.Alive(id) {
		return nil
	}

	// クリーンな終了を grace の間まで待つ。キャンセルは待機を打ち切るが、ここで
	// 早期リターンはしない: ループ条件が偽になり、下の SIGKILL へ早期エスカレートする。
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		if !u.Alive(id) {
			return nil
		}
		select {
		case <-ctx.Done():
			// 次の反復で ctx.Err() != nil によりループを抜け、SIGKILL へ進む。
		case <-time.After(pollInterval):
		}
	}

	// まだ生存している: 強制終了する。
	if u.Alive(id) {
		_ = syscall.Kill(-id.Pgid, syscall.SIGKILL)
		for range 20 {
			if !u.Alive(id) {
				return nil
			}
			time.Sleep(pollInterval)
		}
	}
	if u.Alive(id) {
		// 消滅を確認できなかった。呼び出し元は Running を保持し、失敗を観測可能にする。
		return fmt.Errorf("pgid %d: %w", id.Pgid, ErrStillRunning)
	}
	return nil
}
