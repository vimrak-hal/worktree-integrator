package server

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/vimrak-hal/worktree-integrator/internal/core/cmdspec"
	"github.com/vimrak-hal/worktree-integrator/internal/core/wtenv"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/proc"
)

// SwitchStatus は実行サマリ向けの、サーバー切り替えが成功した場合の結果を表す。
// 失敗した切り替えは status ではなく SwitchServer からのエラーとして報告される。
type SwitchStatus int

const (
	// SwitchStarted: 要求されたワークツリー向けにサーバーを（再）起動した。
	SwitchStarted SwitchStatus = iota
	// SwitchAlreadyRunning: 要求されたワークツリーのサーバーはすでに起動済みだった。
	SwitchAlreadyRunning
)

// StepError は、サーバーのライフサイクルコマンド（setup / on_switch /
// on_activate）が失敗したことを報告する。Cause は元となった実行エラーであり、
// コマンドが最後まで実行されたものの 0 以外で終了した場合は nil となる。
// server サブシステムの他の部分と同様、ユーザー向けのテキストは持たず、
// 表示層がレンダリングする。
type StepError struct {
	Step  string
	Cause error
}

func (e *StepError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Step, e.Cause)
	}
	return e.Step + " exited non-zero"
}

func (e *StepError) Unwrap() error { return e.Cause }

// StartError は、常駐するサーバープロセスの起動に失敗した（または起動直後に
// 死亡した）ことを報告する。即死検出で失敗した場合、LogTail に現行インスタンスの
// ログ末尾（最大 logTailLines 行）が入り、表示層が原因の手掛かりとして提示する。
type StartError struct {
	Cause   error
	LogTail []string
}

func (e *StartError) Error() string { return fmt.Sprintf("start: %v", e.Cause) }

func (e *StartError) Unwrap() error { return e.Cause }

// StopFailedError は、旧インスタンスの停止に失敗したため切り替えを中止したことを
// 報告する。新プロセスは起動されず、Running の記録は保持される（再試行可能）。
type StopFailedError struct {
	Cause error
}

func (e *StopFailedError) Error() string { return fmt.Sprintf("stop previous instance: %v", e.Cause) }

func (e *StopFailedError) Unwrap() error { return e.Cause }

// startupWatch は spawn 後に即死を監視する時間。この間に消滅したサーバーは
// 「起動失敗」としてログ末尾つきで報告される（設定不要の既定動作）。
const startupWatch = 500 * time.Millisecond

// logTailLines は即死検出の StartError に添えるログ末尾の行数。
const logTailLines = 20

// pollInterval は、起動直後の即死監視（watchStartup）で Alive をポーリングする間隔。
const pollInterval = 50 * time.Millisecond

// Status は status 向けの、1 つのサーバーの現在のランタイム状態を表す。
type Status int

const (
	// StatusRunning: 稼働中のサーバー（プロセスグループが生存し、同一性も一致）。
	StatusRunning Status = iota
	// StatusCrashed: 状態にはサーバーが記録されているが、そのプロセスは消滅している
	//（番号だけが再利用された場合も、同一性の不一致によりここに含まれる）。
	StatusCrashed
	// StatusStopped: サーバーが記録されていない。
	StatusStopped
)

// EventKind は、サーバーの切り替えや停止の際に発行される進捗イベントを分類する。
// server サブシステムはユーザー向けのテキストを持たない。何が起きたかを言語に依存しない
// イベントとして報告し、それを表示層がレンダリングする。
// 致命的な失敗はイベントではなくエラーとして報告される。
type EventKind int

const (
	// EventAlreadyRunning: 要求されたワークツリーのサーバーはすでに起動済みだった
	//（Pid）。何もしない切り替えで発行される。
	EventAlreadyRunning EventKind = iota
	// EventStoppingOld: 切り替えや再起動の前に、以前に起動していたインスタンス（Pid）を
	// 停止中。
	EventStoppingOld
	// EventStarted: サーバーを起動した（Pid）。
	EventStarted
	// EventStopped: 稼働中のサーバーの消滅を確認した（Pid）。StopGroup の成功後に
	// のみ発行される（「停止しました」→「停止エラー」の不自然な並びは存在しない）。
	EventStopped
	// EventStopFailed: サーバーの停止に失敗した（Pid, Err）。Running の記録は保持
	// されたままであり、次のコマンドで再試行できる。
	EventStopFailed
	// EventAlreadyStopped: 記録されていたサーバーはすでに停止していた。その記録を
	// クリアした。
	EventAlreadyStopped
)

// Event は、サーバーの切り替えや停止の際に起きた 1 つの事象を表す。どのフィールドが
// 意味を持つかは Kind に依存する。
type Event struct {
	Kind EventKind
	// Pid は対象のプロセス ID（起動／停止イベント）。
	Pid int
	// Err は EventStopFailed における、元となったエラー。
	Err error
}

// SwitchResult は、サーバー切り替えが成功した場合の結果を表す。その status と、
// 報告すべき順序付きのイベントから成る。失敗時は SwitchServer が代わりに
// nil でないエラーを返し、Status は意味を持たない。
type SwitchResult struct {
	Status SwitchStatus
	Events []Event
}

// StopResult は 1 つのサーバーを停止した結果を表す。
type StopResult struct {
	// Stopped は、稼働記録が実際にクリアされた（＝状態が変わった）かどうか。停止に
	// 失敗した場合は false のままで、Running は保持される。呼び出し元はこの値から
	// dirty（永続化の要否）を導出できる。
	Stopped bool
	// Failed は停止を試みたが消滅を確認できなかったかどうか（EventStopFailed と対応）。
	Failed bool
	// Events は報告すべき順序付きのイベント。
	Events []Event
}

// serverEnv はサーバーコマンド向けの WT_* 環境を構築する。共有のワークツリー／リポジトリ
// 変数に加えて WT_SERVER_NAME を含む。
func serverEnv(run *wtenv.RunContext, repo *wtenv.RepoContext, serverName string) []wtenv.Pair {
	env := wtenv.EnvPairs(run, repo)
	return append(env, wtenv.Pair{Key: "WT_SERVER_NAME", Value: serverName})
}

// SwitchRequest は、1 つのサーバー切り替えを表す読み取り専用の入力。
type SwitchRequest struct {
	// Run は実行全体のコンテキスト。その WorktreeName が切り替え対象となる。
	Run *wtenv.RunContext
	// Repo はこのサーバーが属するリポジトリ。
	Repo *wtenv.RepoContext
	// ServerName はリポジトリ内でのサーバー名。
	ServerName string
	// Spec はサーバーの定義。
	Spec Spec
	// ServerCwd はサーバー（およびそのライフサイクルコマンド）を実行する作業
	// ディレクトリ。
	ServerCwd string
	// Log はサーバーの出力を取り込むログファイル。
	Log string
	// Restart は、要求されたワークツリーのサーバーがすでに起動中であっても再起動する。
	Restart bool
}

// SwitchServer は 1 つのサーバーを req.Run.WorktreeName に切り替える。runtime を
// 変更し（呼び出し元が永続化する）、status と報告すべき順序付きのイベントを返す。
// ライフサイクルコマンドや起動の失敗は *StepError / *StartError /
// *StopFailedError として返す。自身では何も出力しない。ctx はライフサイクル
// コマンド・停止処理・起動監視に貫通する（デタッチ起動そのものは ProcessControl の
// 契約どおり ctx の対象外）。
//
// 順序の不変条件: 旧インスタンスの停止は、ライフサイクルコマンド（setup /
// on_switch / on_activate）がすべて成功した後・新プロセスの起動直前に行う。
// 途中で失敗した場合、旧サーバーは止まらずに生き続ける（サービス断を作らない）。
// 停止に失敗した場合は新プロセスを起動しない（二重起動を作らない）。
func SwitchServer(ctx context.Context, pc ProcessControl, runtime *Runtime, req *SwitchRequest) (SwitchResult, error) {
	worktree := req.Run.WorktreeName
	// ライフサイクルコマンドとサーバー起動へ渡す環境を合成する。ドメイン語彙の
	// WT_* ペアを継承環境（os.Environ()）に重ねて "KEY=VALUE" のスライスへ落とし込み、
	// ProcessControl（core を知らない infra 実装）へはこの合成済みの形で渡す。
	env := wtenv.Environ(os.Environ(), serverEnv(req.Run, req.Repo, req.ServerName))
	// 初回判定は記録と現実の両方で行う: setup の記録があっても、その実行先の
	// パスが消えていれば（worktree が削除・再作成されていれば）初回として扱う。
	rec, recorded := runtime.Setup[worktree]
	isFirst := !recorded || !PathExists(rec.Path)

	var events []Event
	emit := func(e Event) { events = append(events, e) }
	result := func(status SwitchStatus) SwitchResult { return SwitchResult{Status: status, Events: events} }
	// fail はそこまでに収集したイベントを致命的なエラーとともに返す。呼び出し元は
	// エラーを確認し、Status は無視する。
	fail := func(err error) (SwitchResult, error) { return SwitchResult{Events: events}, err }

	// runStep は任意のライフサイクルコマンドを実行し、実行できなかった場合や 0 以外で
	// 終了した場合に *StepError を返す（これにより呼び出し元はこのサーバーを中止する）。
	runStep := func(step string, command *cmdspec.Commands) error {
		if command == nil {
			return nil
		}
		switch ok, err := pc.RunForeground(ctx, command.Script(), req.ServerCwd, env); {
		case err != nil:
			return &StepError{Step: step, Cause: err}
		case !ok:
			return &StepError{Step: step}
		default:
			return nil
		}
	}

	// 要求されたワークツリーをすでに実行中か？ 判定は Instance.Worktree（稼働
	// インスタンスの属性）だけで core 内に閉じる。--restart でない限りそのままにする。
	if runtime.Running != nil && runtime.Running.Worktree == worktree {
		if pc.Alive(runtime.Running.Ident) && !req.Restart {
			emit(Event{Kind: EventAlreadyRunning, Pid: runtime.Running.Ident.Pid})
			return result(SwitchAlreadyRunning), nil
		}
	}

	// ライフサイクル: 初回アクティベーションでは setup を実行し、切り替えでは on_switch を
	// 実行する。いずれの場合も、その後サーバーの起動直前に on_activate を実行する。
	// この間、旧インスタンスは動き続ける（停止は全ステップ成功後）。
	if isFirst {
		if err := runStep("setup", req.Spec.Setup); err != nil {
			return fail(err)
		}
		runtime.RecordSetup(worktree, req.Repo.WorktreePath)
	} else if err := runStep("on_switch", req.Spec.OnSwitch); err != nil {
		return fail(err)
	}

	if err := runStep("on_activate", req.Spec.OnActivate); err != nil {
		return fail(err)
	}

	// 旧インスタンス（以前のワークツリー、または再起動対象）を停止する。grace は
	// 起動時に凍結された値を使う。停止に失敗したら Running を保持したまま中止し、
	// 新プロセスは起動しない。
	if runtime.Running != nil {
		running := runtime.Running
		if pc.Alive(running.Ident) {
			emit(Event{Kind: EventStoppingOld, Pid: running.Ident.Pid})
			if err := pc.StopGroup(ctx, running.Ident, running.Grace()); err != nil {
				emit(Event{Kind: EventStopFailed, Pid: running.Ident.Pid, Err: err})
				return fail(&StopFailedError{Cause: err})
			}
			emit(Event{Kind: EventStopped, Pid: running.Ident.Pid})
		}
		// 消滅を確認できた（または既に消滅していた = クラッシュの自己修復）。
		runtime.Running = nil
	}

	// 長時間稼働するサーバーをデタッチして起動する（ログのローテートとログ
	// ディレクトリの作成は SpawnDetached が行う）。
	id, err := pc.SpawnDetached(req.Spec.Start.Script(), req.ServerCwd, env, req.Log)
	if err != nil {
		return fail(&StartError{Cause: err})
	}
	// 即死検出: 起動直後の短い監視で消滅したら、ログ末尾を添えて失敗にする。
	if err := watchStartup(ctx, pc, id, req.Log); err != nil {
		return fail(err)
	}
	emit(Event{Kind: EventStarted, Pid: id.Pid})
	runtime.Running = &Instance{
		Ident:     id,
		Worktree:  worktree,
		Log:       req.Log,
		GraceSecs: uint64(req.Spec.Grace() / time.Second),
		StartedAt: nowUnix(),
	}
	// クラッシュ・停止で Running が消えた後も logs が直近のログへ辿り着けるように、
	// 最後に起動したログパスを別途記録する。
	runtime.LastLog = req.Log
	return result(SwitchStarted), nil
}

// watchStartup は spawn 直後の即死を検出する。startupWatch のあいだ Alive を
// ポーリングし、消滅を検知したらログ末尾を添えた *StartError を返す。ctx の
// キャンセルは監視の放棄であり、起動失敗ではない（サーバーはデタッチ済みで、
// その時点までは生存が確認されている）。
func watchStartup(ctx context.Context, pc ProcessControl, id proc.Ident, logPath string) error {
	deadline := time.Now().Add(startupWatch)
	for {
		if !pc.Alive(id) {
			return &StartError{
				Cause:   fmt.Errorf("server process (pid %d) exited immediately after start", id.Pid),
				LogTail: ReadTail(logPath, logTailLines),
			}
		}
		if time.Now().After(deadline) {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(pollInterval):
		}
	}
}

// ReadTail は path の末尾 n 行を返す。読めない場合（ログ未作成など）は nil。
// 即死検出の報告や logs ワークフローで使う小さなヘルパーで、巨大なログを想定しない。
func ReadTail(path string, n int) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return TailLines(data, n)
}

// TailLines は data を "\n" で分割した末尾 n 行を返す。末尾の改行は無視され、
// 余計な空の最終行を生じさせない。n が 0 以下なら何も返さない。
func TailLines(data []byte, n int) []string {
	if n <= 0 {
		return nil
	}
	text := strings.TrimRight(string(data), "\n")
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	if start := len(lines) - n; start > 0 {
		lines = lines[start:]
	}
	return lines
}

// PathExists は path が存在するかどうかを返す（シンボリックリンクをたどる）。
// isFirst 判定や logs ワークフローが、記録をファイルシステムの現実と突き合わせる
// ために使う。
func PathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Probe はサーバーの status を判定し、クラッシュした（消滅した、または番号が
// 再利用されただけの）running エントリをクリアして自己修復する。返される bool は
// エントリが変更されたかどうかを報告し、呼び出し元が永続化すべきか判断できるように
// する。ctx は状態機械の公開エントリポイント（SwitchServer / StopServer / Probe）で
// 署名を揃えるために受け取る。現在の実装はブロックしない検査のみで ctx を参照しない。
func Probe(_ context.Context, pc ProcessControl, runtime *Runtime) (state Status, pid int, changed bool) {
	if runtime.Running == nil {
		return StatusStopped, 0, false
	}
	if pc.Alive(runtime.Running.Ident) {
		return StatusRunning, runtime.Running.Ident.Pid, false
	}
	runtime.Running = nil
	return StatusCrashed, 0, true
}

// StopServer はサーバーが記録されていればそれを停止し、runtime を変更する（呼び出し元が
// 永続化する）。grace は起動時に凍結された Instance.GraceSecs を使う。
//
// 不変条件: Running の記録は、プロセスの消滅を確認できた場合にのみクリアされる。
// 停止に失敗した場合は記録を保持し（Failed=true, EventStopFailed）、孤児を台帳から
// 消さない。次のコマンドで再試行できる。
func StopServer(ctx context.Context, pc ProcessControl, runtime *Runtime) StopResult {
	if runtime.Running == nil {
		return StopResult{}
	}
	running := runtime.Running
	if !pc.Alive(running.Ident) {
		// すでに消滅している（クラッシュ、または番号だけの再利用）。記録だけを消す。
		runtime.Running = nil
		return StopResult{Stopped: true, Events: []Event{{Kind: EventAlreadyStopped}}}
	}
	if err := pc.StopGroup(ctx, running.Ident, running.Grace()); err != nil {
		return StopResult{Failed: true, Events: []Event{{Kind: EventStopFailed, Pid: running.Ident.Pid, Err: err}}}
	}
	runtime.Running = nil
	return StopResult{Stopped: true, Events: []Event{{Kind: EventStopped, Pid: running.Ident.Pid}}}
}
