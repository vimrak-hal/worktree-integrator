package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/vimrak-hal/worktree-integrator/internal/app/tree"
)

// 2 ペインは 1 画面にツリーのノード名を左に、右ペインを "│" 区切りで同時に描く。
func TestViewShowsBothPanes(t *testing.T) {
	m := newTestModel(t)
	m.cfg = serverCfg()
	m.trees = treesResult(tree.WorktreeRow{Name: "feat-a", Repos: []tree.RepoCell{{Repo: "api"}}})
	m.buildNodes()

	view := m.View()
	for _, want := range []string{"feat-a", "api/backend", "│"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q", want)
		}
	}
}

// padDisplay は色付き（ANSI エスケープを含む）文字列でも表示幅をちょうど w にする。
func TestPadDisplayFixesWidth(t *testing.T) {
	colored := styFlag.Render("api/backend")
	got := padDisplay(colored, 30)
	if w := lipgloss.Width(got); w != 30 {
		t.Fatalf("padDisplay width = %d, want 30", w)
	}
	// 幅を超える入力は切り詰めて w に収める。
	long := padDisplay(strings.Repeat("x", 100), 30)
	if w := lipgloss.Width(long); w != 30 {
		t.Fatalf("padDisplay truncated width = %d, want 30", w)
	}
}
