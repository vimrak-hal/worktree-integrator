// Package serverfake は、実際のプロセスに触れずにサーバーの切り替え／停止／状態の
// ロジックとオーケストレーションをテストするための、インメモリの ProcessControl
// ダブルを提供する。テストファイルからのみインポートされる。
//
// 実ファイルシステムには一切書き込まない（SpawnDetached はログファイルを作らない。
// ログの内容を検証するテストは、渡したログパスへ自分でファイルを書く）。内部状態は
// ミューテックスで保護されており、並行テスト（-race）から安全に使える。
package serverfake

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/vimrak-hal/worktree-integrator/internal/infra/proc"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/wtenv"
)

// Fake は、インメモリの生存マップに支えられた ProcessControl。起動された
// 「プロセス」は、StopGroup が死んだと印を付けるまで生存している。Ident の
// StartUnixMs はカウンタで払い出され、実装は本物と同じく「pid と開始時刻の両方が
// 一致して初めて同一」の照合を行う。そのため、同じ pid に別の開始時刻を持つ Ident
// （= 番号の再利用をシミュレートしたもの）は Alive が false になり、StopGroup は
// シグナルを送らない。
type Fake struct {
	mu           sync.Mutex
	alive        map[int]bool
	start        map[int]int64 // pid → 払い出した架空の開始時刻（エポックミリ秒）
	nextPID      int
	nextStart    int64
	foregroundOK bool

	// DieOnSpawn が true のとき、SpawnDetached で起動した「プロセス」は即座に死亡
	// する（即死検出のテスト用）。
	DieOnSpawn bool
	// StopError が非 nil のとき、StopGroup は（同一性が一致していても）その
	// エラーを返し、プロセスを生存したままにする（停止失敗のテスト用）。
	StopError error

	foregroundRuns []string
	spawns         []string
	stopSignals    []int // 同一性が一致し、実際に「シグナルを送った」pgid の記録
	lastGrace      time.Duration
}

// New は、ライフサイクルコマンドが成功するフェイクを返す。
func New() *Fake {
	return &Fake{
		alive: map[int]bool{}, start: map[int]int64{},
		nextPID: 1000, nextStart: 1_700_000_000_000, foregroundOK: true,
	}
}

// Failing は、フォアグラウンド（ライフサイクル）コマンドが常に失敗するフェイクを返す。
func Failing() *Fake {
	f := New()
	f.foregroundOK = false
	return f
}

// FailForeground は、以降のフォアグラウンドコマンドを失敗（exit 非 0 相当）させる。
// 「初回 switch は成功、次の switch のライフサイクルで失敗」のような遷移を組み立てる
// テスト用。
func (f *Fake) FailForeground() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.foregroundOK = false
}

// ForegroundRuns は RunForeground に渡されたスクリプトのスナップショットを返す。
func (f *Fake) ForegroundRuns() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.foregroundRuns)
}

// Spawns は SpawnDetached に渡されたスクリプトのスナップショットを返す。
func (f *Fake) Spawns() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.spawns)
}

// StopSignals は、同一性検証を通過して実際にシグナルが送られた pgid の一覧を返す。
// 同一性が一致しない StopGroup 呼び出しはここに現れない（誤殺防止の検証用）。
func (f *Fake) StopSignals() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.stopSignals)
}

// LastGrace は、最後に StopGroup へ渡された grace を返す（GraceSecs 凍結の検証用）。
func (f *Fake) LastGrace() time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastGrace
}

func (f *Fake) RunForeground(_ context.Context, script, _ string, _ []wtenv.Pair) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.foregroundRuns = append(f.foregroundRuns, script)
	return f.foregroundOK, nil
}

func (f *Fake) SpawnDetached(script, _ string, _ []wtenv.Pair, _ string) (proc.Ident, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextPID++
	f.nextStart++
	pid := f.nextPID
	f.alive[pid] = !f.DieOnSpawn
	f.start[pid] = f.nextStart
	f.spawns = append(f.spawns, script)
	return proc.Ident{Pid: pid, Pgid: pid, StartUnixMs: f.nextStart}, nil
}

// same は id が「このフェイクが払い出したその pid のプロセス」と同一かを報告する
// （呼び出し側がロックを保持していること）。
func (f *Fake) same(id proc.Ident) bool {
	start, ok := f.start[id.Pid]
	return ok && start == id.StartUnixMs
}

func (f *Fake) Alive(id proc.Ident) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.alive[id.Pgid] && f.same(id)
}

func (f *Fake) StopGroup(_ context.Context, id proc.Ident, grace time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastGrace = grace
	// 実装と同じ契約: シグナル送出前に同一性を検証し、一致しなければ何も送らずに
	// 成功とする（対象は既に消滅している）。
	if !f.alive[id.Pgid] || !f.same(id) {
		return nil
	}
	if f.StopError != nil {
		return f.StopError
	}
	f.stopSignals = append(f.stopSignals, id.Pgid)
	f.alive[id.Pgid] = false
	return nil
}
