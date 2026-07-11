package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vimrak-hal/worktree-integrator/internal/app/server"
	"github.com/vimrak-hal/worktree-integrator/internal/app/tree"
	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
	coreserver "github.com/vimrak-hal/worktree-integrator/internal/core/server"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/statedir"
)

// newTestModel は端末サイズ設定済みの（コマンドを一切走らせていない）モデルを返す。
// 統合的な I/O（resolve・switch）はワークフロー側のテストが担っており、ここでは
// モデルの状態遷移だけを検証する。
func newTestModel(t *testing.T) *model {
	t.Helper()
	m := newModel(t.Context(), &config.File{}, statedir.Root{}, &forwarder{})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	return m
}

// serverCfg は api リポジトリに backend / web の 2 サーバーを持つ設定を返す。Spec は
// 空でよい（buildNodes はサーバー名の集合しか見ず、Validate は呼ばない）。
func serverCfg() *config.File {
	return &config.File{Repos: map[string]config.RepoConfig{
		"api": {Servers: map[string]coreserver.Spec{"backend": {}, "web": {}}},
	}}
}

func treesResult(rows ...tree.WorktreeRow) *tree.ListResult {
	return &tree.ListResult{Worktrees: rows}
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

// buildNodes は worktree ノードの配下に、メンバー repo に設定された全サーバーを
// repo → server の順で並べ、稼働中サーバーにはそのフィールドを立てる。
func TestBuildNodesLayout(t *testing.T) {
	m := newTestModel(t)
	m.cfg = serverCfg()
	m.trees = treesResult(
		tree.WorktreeRow{Name: "feat-a", Repos: []tree.RepoCell{{Repo: "api"}},
			Servers: []tree.ServerCell{{Repo: "api", Server: "backend", Pid: 4242}}},
		tree.WorktreeRow{Name: "feat-b", Repos: []tree.RepoCell{{Repo: "api"}}},
	)
	m.buildNodes()

	want := []struct {
		key     string
		running bool
	}{
		{"feat-a", false},
		{"feat-a\x00api/backend", true},
		{"feat-a\x00api/web", false},
		{"feat-b", false},
		{"feat-b\x00api/backend", false},
		{"feat-b\x00api/web", false},
	}
	if len(m.nodes) != len(want) {
		t.Fatalf("nodes = %d, want %d", len(m.nodes), len(want))
	}
	for i, w := range want {
		if m.nodes[i].key() != w.key {
			t.Fatalf("node[%d].key = %q, want %q", i, m.nodes[i].key(), w.key)
		}
		if m.nodes[i].running != w.running {
			t.Fatalf("node[%d].running = %v, want %v", i, m.nodes[i].running, w.running)
		}
	}
	if got := m.nodes[1]; got.pid != 4242 {
		t.Fatalf("running server pid = %d, want 4242", got.pid)
	}
}

// サーバーノードへ移動すると表示ログ対象（curKey）が変わり、worktree ノード上では
// 対象が維持される。
func TestMoveSelUpdatesTarget(t *testing.T) {
	m := newTestModel(t)
	m.cfg = serverCfg()
	m.trees = treesResult(
		tree.WorktreeRow{Name: "feat-a", Repos: []tree.RepoCell{{Repo: "api"}}},
		tree.WorktreeRow{Name: "feat-b", Repos: []tree.RepoCell{{Repo: "api"}}},
	)
	m.buildNodes()

	m.moveSel(1) // feat-a/api/backend
	if m.curKey != "feat-a\x00api/backend" {
		t.Fatalf("curKey after first move = %q", m.curKey)
	}
	m.moveSel(1) // feat-a/api/web
	if m.curKey != "feat-a\x00api/web" {
		t.Fatalf("curKey after second move = %q", m.curKey)
	}
	held := m.curKey
	m.moveSel(1) // feat-b (worktree ノード)
	if !m.nodes[m.sel].isWorktree() {
		t.Fatalf("expected cursor on a worktree node, got %+v", m.nodes[m.sel])
	}
	if m.curKey != held {
		t.Fatalf("worktree ノード上で curKey が変わった: %q → %q", held, m.curKey)
	}
}

// パスが変わった対象（外部の switch・--prev トグル）はバッファごと読み直しになる。
func TestApplyResolvedResetsOnPathChange(t *testing.T) {
	m := newTestModel(t)
	dir := t.TempDir()
	oldLog := filepath.Join(dir, "feat-a.log")
	newLog := filepath.Join(dir, "feat-b.log")
	os.WriteFile(oldLog, []byte("old line\n"), 0o644)
	os.WriteFile(newLog, []byte("new line\n"), 0o644)

	k := "feat-a\x00api/backend"
	m.curKey = k
	m.applyResolved(resolvedMsg{selKey: k, path: oldLog, status: &server.StatusResult{}})
	if got := m.bufs[k].slice(); len(got) != 1 || got[0] != "old line" {
		t.Fatalf("buffer = %+v", got)
	}

	// 外部（MCP など）で switch が起きるとログパスが変わる。バッファは新ログの
	// 内容だけになる。
	m.applyResolved(resolvedMsg{selKey: k, path: newLog, status: &server.StatusResult{}})
	if got := m.bufs[k].slice(); len(got) != 1 || got[0] != "new line" {
		t.Fatalf("buffer after path change = %+v", got)
	}
}

func TestFilterNarrowsRenderedLines(t *testing.T) {
	m := newTestModel(t)
	k := "feat-a\x00api/backend"
	m.curKey = k
	log := filepath.Join(t.TempDir(), "a.log")
	os.WriteFile(log, []byte("GET /healthz 200\nERROR boom\nGET /users 200\n"), 0o644)
	m.applyResolved(resolvedMsg{selKey: k, path: log, status: &server.StatusResult{}})

	m.filter = "error"
	m.rebuildLog()
	if view := m.vp.View(); !strings.Contains(view, "boom") || strings.Contains(view, "healthz") {
		t.Fatalf("filtered view = %q", view)
	}
}

// 上方向のスクロールは追従を解除し、f で再開する（ログペインにフォーカス時）。
func TestScrollDisablesFollow(t *testing.T) {
	m := newTestModel(t)
	m.focus = focusLog
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

// B2: 新しい seq の path 適用後、古い seq の missing=true が届いてもバッファは消えない
// （発行順の世代で古い解決を弾く）。
func TestApplyResolvedIgnoresStaleSeq(t *testing.T) {
	m := newTestModel(t)
	log := filepath.Join(t.TempDir(), "a.log")
	os.WriteFile(log, []byte("live line\n"), 0o644)
	k := "feat-a\x00api/backend"
	m.curKey = k

	// seq=2 で path を適用（バッファ生成）。
	m.applyResolved(resolvedMsg{seq: 2, selKey: k, path: log, status: &server.StatusResult{}})
	if m.bufs[k] == nil || m.tails[k] == nil {
		t.Fatal("path resolution must create a tailer and a buffer")
	}

	// 遅れて届いた古い seq=1 の missing。無視されバッファ・tailer は残る。
	m.applyResolved(resolvedMsg{seq: 1, selKey: k, missing: true, status: &server.StatusResult{}})
	if m.bufs[k] == nil || m.tails[k] == nil {
		t.Fatal("stale resolution must not delete tails/bufs")
	}
	if m.curMissing {
		t.Fatal("stale missing must not mark the target missing")
	}
}

// B3: ノードが消えると buildNodes が該当 key の tailer / バッファを掃除する
// （worktree の作成→削除の繰り返しでリングが残り続けるのを防ぐ）。
func TestBuildNodesEvictsGoneTargets(t *testing.T) {
	m := newTestModel(t)
	m.cfg = serverCfg()
	gone := "feat-a\x00api/backend"
	m.tails[gone] = newTailer("/does-not-matter")
	m.bufs[gone] = newRing(targetRingCap)
	m.curKey = "" // 消える key を保護しない

	// feat-a を含まない一覧で再構築 → gone のノードは存在しない。
	m.trees = treesResult(tree.WorktreeRow{Name: "feat-b", Repos: []tree.RepoCell{{Repo: "api"}}})
	m.buildNodes()

	if _, ok := m.tails[gone]; ok {
		t.Fatal("gone target tailer must be evicted")
	}
	if _, ok := m.bufs[gone]; ok {
		t.Fatal("gone target buffer must be evicted")
	}
}

// C5: follow オフで YOffset を進めた状態のリサイズは YOffset を保つ（作り直すと 0 に戻る）。
func TestWindowResizePreservesYOffset(t *testing.T) {
	m := newTestModel(t)
	k := "feat-a\x00api/backend"
	m.curKey = k
	log := filepath.Join(t.TempDir(), "a.log")
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString("line\n")
	}
	os.WriteFile(log, []byte(b.String()), 0o644)
	m.applyResolved(resolvedMsg{selKey: k, path: log, status: &server.StatusResult{}})

	m.focus = focusLog
	m.follow = false
	m.vp.SetYOffset(50)
	off := m.vp.YOffset
	if off == 0 {
		t.Fatal("test setup: YOffset must be advanced")
	}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	if m.vp.YOffset != off {
		t.Fatalf("resize must preserve YOffset, got %d want %d", m.vp.YOffset, off)
	}
}
