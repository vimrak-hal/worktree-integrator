package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vimrak-hal/worktree-integrator/internal/app/tree"
	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
	coreserver "github.com/vimrak-hal/worktree-integrator/internal/core/server"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/statedir"
)

// newTestModel は端末サイズ設定済みの（コマンドを一切走らせていない）モデルを返す。
// 統合的な I/O（resolve）はワークフロー側のテストが担っており、ここでは
// モデルの状態遷移だけを検証する。
func newTestModel(t *testing.T) *model {
	t.Helper()
	m := newModel(t.Context(), &config.File{}, statedir.Root{})
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

// moveSel はツリーのカーソル（sel）を上下に動かし、両端でクランプする。
func TestMoveSelMovesCursor(t *testing.T) {
	m := newTestModel(t)
	m.cfg = serverCfg()
	m.trees = treesResult(
		tree.WorktreeRow{Name: "feat-a", Repos: []tree.RepoCell{{Repo: "api"}}},
		tree.WorktreeRow{Name: "feat-b", Repos: []tree.RepoCell{{Repo: "api"}}},
	)
	m.buildNodes()

	if m.sel != 0 {
		t.Fatalf("initial sel = %d, want 0", m.sel)
	}
	m.moveSel(1)
	if m.sel != 1 {
		t.Fatalf("sel after down = %d, want 1", m.sel)
	}
	// 先頭より上へは動かない（クランプ）。
	m.moveSel(-5)
	if m.sel != 0 {
		t.Fatalf("sel clamped low = %d, want 0", m.sel)
	}
	// 末尾より下へは動かない。
	m.moveSel(100)
	if want := len(m.nodes) - 1; m.sel != want {
		t.Fatalf("sel clamped high = %d, want %d", m.sel, want)
	}
}
