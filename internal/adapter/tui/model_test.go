package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vimrak-hal/worktree-integrator/internal/app/server"
	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/statedir"
)

// newTestModel は端末サイズ設定済みの（コマンドを一切走らせていない）モデルを返す。
// 統合的な I/O（resolve・switch）はワークフロー側のテストが担っており、ここでは
// モデルの状態遷移だけを検証する。
func newTestModel(t *testing.T) *model {
	t.Helper()
	m := newModel(t.Context(), &config.File{}, statedir.Root{}, &forwarder{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	return m
}

// resolved は logTarget 相当のエントリから resolvedMsg を組み立てる。
func resolved(entries ...server.LogEntry) resolvedMsg {
	return resolvedMsg{
		cfg:    &config.File{},
		logs:   &server.LogsResult{Logs: entries},
		status: &server.StatusResult{},
	}
}

func key(msg string) tea.KeyMsg {
	if len(msg) == 1 {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(msg)}
	}
	switch msg {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEscape}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	}
	panic("unknown key " + msg)
}

func TestViewKeysSwitchViews(t *testing.T) {
	m := newTestModel(t)
	if m.view != viewLogs {
		t.Fatalf("initial view = %d", m.view)
	}
	m.Update(key("2"))
	if m.view != viewStatus {
		t.Fatalf("after '2' view = %d", m.view)
	}
	m.Update(key("tab"))
	if m.view != viewTrees {
		t.Fatalf("after tab view = %d", m.view)
	}
	m.Update(key("tab"))
	if m.view != viewLogs {
		t.Fatalf("tab must wrap to logs, got %d", m.view)
	}
}

func TestCycleTargetWrapsThroughMerged(t *testing.T) {
	m := newTestModel(t)
	log1 := filepath.Join(t.TempDir(), "a.log")
	log2 := filepath.Join(t.TempDir(), "b.log")
	os.WriteFile(log1, []byte("l1\n"), 0o644)
	os.WriteFile(log2, []byte("l2\n"), 0o644)
	m.applyResolved(resolved(
		server.LogEntry{Repo: "api", Server: "backend", Path: log1},
		server.LogEntry{Repo: "web", Server: "frontend", Path: log2},
	))

	if m.selKey != "" {
		t.Fatalf("initial selection must be merged, got %q", m.selKey)
	}
	m.cycleTarget(+1)
	if m.selKey != "api/backend" {
		t.Fatalf("selKey = %q", m.selKey)
	}
	m.cycleTarget(+1)
	if m.selKey != "web/frontend" {
		t.Fatalf("selKey = %q", m.selKey)
	}
	m.cycleTarget(+1)
	if m.selKey != "" {
		t.Fatalf("cycle must wrap back to merged, got %q", m.selKey)
	}
	m.cycleTarget(-1)
	if m.selKey != "web/frontend" {
		t.Fatalf("backward cycle selKey = %q", m.selKey)
	}
}

// パスが変わった対象（外部の switch・--prev トグル）はバッファごと読み直しになり、
// 対象自体が消えた場合は選択がマージ表示へ戻る。
func TestApplyResolvedResetsOnPathChange(t *testing.T) {
	m := newTestModel(t)
	dir := t.TempDir()
	oldLog := filepath.Join(dir, "feat-a.log")
	newLog := filepath.Join(dir, "feat-b.log")
	os.WriteFile(oldLog, []byte("old line\n"), 0o644)
	os.WriteFile(newLog, []byte("new line\n"), 0o644)

	m.applyResolved(resolved(server.LogEntry{Repo: "api", Server: "backend", Path: oldLog}))
	m.selKey = "api/backend"
	if got := m.bufs["api/backend"].slice(); len(got) != 1 || got[0].text != "old line" {
		t.Fatalf("buffer = %+v", got)
	}

	// 外部（MCP など）で switch が起きるとログパスが変わる。バッファは新ログの
	// 内容だけになる。
	m.applyResolved(resolved(server.LogEntry{Repo: "api", Server: "backend", Path: newLog}))
	if got := m.bufs["api/backend"].slice(); len(got) != 1 || got[0].text != "new line" {
		t.Fatalf("buffer after path change = %+v", got)
	}
	// マージバッファは切り替えをまたいだ時系列を保持する。
	if got := m.merged.slice(); len(got) != 2 {
		t.Fatalf("merged = %+v", got)
	}

	// 対象が消えたら選択はマージ表示へ戻り、追跡も止まる。
	m.applyResolved(resolved())
	if m.selKey != "" || len(m.tails) != 0 || len(m.bufs) != 0 {
		t.Fatalf("stale target must be dropped: selKey=%q tails=%d bufs=%d", m.selKey, len(m.tails), len(m.bufs))
	}
}

func TestFilterNarrowsRenderedLines(t *testing.T) {
	m := newTestModel(t)
	log1 := filepath.Join(t.TempDir(), "a.log")
	os.WriteFile(log1, []byte("GET /healthz 200\nERROR boom\nGET /users 200\n"), 0o644)
	m.applyResolved(resolved(server.LogEntry{Repo: "api", Server: "backend", Path: log1}))

	m.filter = "error"
	m.rebuildLog()
	if view := m.vp.View(); !strings.Contains(view, "boom") || strings.Contains(view, "healthz") {
		t.Fatalf("filtered view = %q", view)
	}
}

// 上方向のスクロールは追従を解除し、f で再開する。
func TestScrollDisablesFollow(t *testing.T) {
	m := newTestModel(t)
	if !m.follow {
		t.Fatal("follow must start enabled")
	}
	m.Update(key("k"))
	if m.follow {
		t.Fatal("scrolling up must disable follow")
	}
	m.Update(key("f"))
	if !m.follow {
		t.Fatal("'f' must re-enable follow")
	}
}

// 操作の実行中は q が完了待ちになり、完了メッセージで終了する。
func TestQuitWaitsForRunningOperation(t *testing.T) {
	m := newTestModel(t)
	m.opRunning = true
	if _, cmd := m.Update(key("q")); cmd != nil {
		t.Fatal("q during an operation must not quit immediately")
	}
	if !m.quitAfterOp {
		t.Fatal("q during an operation must arm quitAfterOp")
	}
	_, cmd := m.Update(opDoneMsg{summary: "done"})
	if cmd == nil {
		t.Fatal("operation completion must quit when quitAfterOp is armed")
	}
	if msg := cmd(); msg != tea.Quit() {
		t.Fatalf("expected tea.Quit, got %#v", msg)
	}
}
