package render

import (
	"fmt"
	"io"
	"sync"

	"github.com/vimrak-hal/worktree-integrator/internal/core/git/worktree"
	coreserver "github.com/vimrak-hal/worktree-integrator/internal/core/server"
)

// Progress はワークフローの途中経過を w へ逐次描画する app.Progress の実装である。
// 出力はミューテックスで直列化され、並行する goroutine（worktree の並列作成）からの
// 行が決して混ざらない。CLI は stdout に、MCP は取り込みバッファに繋ぐ。
type Progress struct {
	mu sync.Mutex
	w  io.Writer
}

// NewProgress は w へ描画する Progress を返す。
func NewProgress(w io.Writer) *Progress {
	return &Progress{w: w}
}

// Update は repo の作成進捗の遷移を 1 行で書き出す。
func (p *Progress) Update(repo string, state worktree.Progress) {
	p.mu.Lock()
	defer p.mu.Unlock()
	tagged(p.w, repo, "%s", progressLabel(state))
}

// Event は repo の型付き途中経過イベントを 1 行で書き出す。
func (p *Progress) Event(repo string, n worktree.Note) {
	p.mu.Lock()
	defer p.mu.Unlock()
	tagged(p.w, repo, "%s", noteLine(n))
}

// ServerEvent はサーバーのライフサイクルイベントを repo/server タグ付きの 1 行で
// 書き出す。
func (p *Progress) ServerEvent(repo, server string, ev coreserver.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Fprint(p.w, serverEventLine(repo+"/"+server, ev))
}

// progressLabel は worktree の進捗状態をユーザー向けの（日本語の）ラベルに変換する。
// Progress は封印された列挙（途中経過のみ）のため、未知の値はバグでありパニックさせる。
func progressLabel(state worktree.Progress) string {
	switch state {
	case worktree.ProgressFetching:
		return "fetch中"
	case worktree.ProgressCreating:
		return "作成中"
	default:
		panic(fmt.Sprintf("unknown worktree.Progress %d", state))
	}
}

// noteLine は型付き途中経過イベントをユーザー向けの 1 行に変換する。NoteKind は
// 封印された列挙のため、未知の値はバグでありパニックさせる。
func noteLine(n worktree.Note) string {
	switch n.Kind {
	case worktree.NoteCopyRejected:
		return fmt.Sprintf("コピー対象をスキップ（不正なパス）: %s", n.Path)
	case worktree.NoteCopyFailed:
		return fmt.Sprintf("コピー失敗 %s: %v", n.Path, n.Err)
	case worktree.NoteGitignoreListFailed:
		return fmt.Sprintf("gitignore の列挙に失敗（自動コピーをスキップ）: %v", n.Err)
	case worktree.NoteFetchDegraded:
		return fmt.Sprintf("fetch に失敗したため、既存の追跡ブランチから作成します: %v", n.Err)
	default:
		panic(fmt.Sprintf("unknown worktree.NoteKind %d", n.Kind))
	}
}

// serverEventLine は 1 つのサーバーイベントをタグ付きの行に整形する。ライブ表示
// （Progress.ServerEvent）が使い、最終描画（render.Switch など）はイベントを重複
// 描画しない — イベントの文言はこの 1 箇所にのみ存在する。EventKind は封印された
// 列挙のため、未知の値はバグでありパニックさせる。
func serverEventLine(tag string, ev coreserver.Event) string {
	switch ev.Kind {
	case coreserver.EventAlreadyRunning:
		return fmt.Sprintf("  [%s] 既に起動中 (pid %d)\n", tag, ev.Pid)
	case coreserver.EventStoppingOld:
		return fmt.Sprintf("  [%s] 旧サーバー停止 (pid %d)\n", tag, ev.Pid)
	case coreserver.EventStarted:
		return fmt.Sprintf("  [%s] 起動 (pid %d)\n", tag, ev.Pid)
	case coreserver.EventStopped:
		return fmt.Sprintf("  [%s] 停止 (pid %d)\n", tag, ev.Pid)
	case coreserver.EventStopFailed:
		return fmt.Sprintf("  [%s] 停止失敗 (pid %d): %v（記録は保持されます。再実行で再試行できます）\n", tag, ev.Pid, ev.Err)
	case coreserver.EventAlreadyStopped:
		return fmt.Sprintf("  [%s] 既に停止済み (記録を消去)\n", tag)
	default:
		panic(fmt.Sprintf("unknown coreserver.EventKind %d", ev.Kind))
	}
}
