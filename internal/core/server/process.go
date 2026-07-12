package server

import (
	"context"
	"time"

	"github.com/vimrak-hal/worktree-integrator/internal/infra/proc"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/procctl"
)

// ErrStillRunning は、SIGKILL 後もプロセスグループの消滅を確認できなかったことを
// 表す。StopGroup は消滅を確認できた場合のみ nil を返す。この失敗を受け取った
// 状態機械は Running の記録を保持する（孤児を台帳から消さない。次回コマンドで
// 再試行できる）。実体は本番実装 procctl の sentinel で、状態機械・テストは
// この別名を通じて errors.Is で判別する。
var ErrStillRunning = procctl.ErrStillRunning

// ProcessControl は、server サブシステムが必要とする OS のプロセス操作を抽象化し、
// activate ロジックをフェイクでテスト可能にする。生存確認と停止は pgid の数値では
// なく proc.Ident（PID + 開始時刻）を受け取り、実装はシグナル送出前に必ず同一性を
// 検証する。番号の再利用による無関係プロセスの誤殺を、インターフェイスの形で防ぐ。
//
// 本番実装は infra/procctl.UnixProcess（OS 依存）であり、テストは serverfake が
// インメモリで差し替える。環境変数はドメイン語彙 wtenv.Pair ではなく、合成済みの
// "KEY=VALUE" 文字列スライス（exec.Cmd.Env と同形）で受け渡す。これによりインターフェイス
// と本番実装が core のドメイン語彙に依存せず、層の依存を core → infra の一方向に保つ。
type ProcessControl interface {
	// RunForeground は短命なコマンドを cwd で env を用いて完了まで実行し、正常に
	// 終了したかどうかを返す。エラーはそもそも実行できなかった、または ctx で
	// キャンセルされたことを意味する（0 以外の終了は ok=false として報告され、
	// エラーにはならない）。
	RunForeground(ctx context.Context, script, cwd string, env []string) (bool, error)
	// SpawnDetached は script を、自身のセッション／プロセスグループにデタッチした
	// サーバーとして起動し、stdout と stderr を logPath へ書き込む。logPath に既存の
	// ログがあれば <logPath>.prev へローテートし（1 世代保持）、「現在のログ =
	// 現行インスタンスの出力」という不変条件を保つ。
	// デタッチして呼び出し元より長く生きることが仕様であるため、意図的に ctx の
	// 対象外である（キャンセルで起動済みサーバーを殺してはならない）。
	SpawnDetached(script, cwd string, env []string, logPath string) (proc.Ident, error)
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
