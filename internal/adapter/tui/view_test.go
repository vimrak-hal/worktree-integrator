package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/vimrak-hal/worktree-integrator/internal/app/tree"
)

// 2 ペインは 1 画面にツリーのノード名とログ行を "│" 区切りで同時に描く。
func TestViewShowsBothPanes(t *testing.T) {
	m := newTestModel(t)
	m.cfg = serverCfg()
	m.trees = treesResult(tree.WorktreeRow{Name: "feat-a", Repos: []tree.RepoCell{{Repo: "api"}}})
	m.buildNodes()
	m.curKey = "feat-a\x00api/backend"
	m.bufs[m.curKey] = newRing(10)
	m.bufs[m.curKey].push("hello-log-line")
	m.rebuildLog()

	view := m.View()
	for _, want := range []string{"feat-a", "api/backend", "hello-log-line", "│"} {
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

// ヘルプ行はフォーカス文脈ごとに主要な操作ラベルを bubbles/help で描く。ここでは
// helpLine のハードコードを keymap + help へ移した後も、文脈別の項目が欠けていない
// ことを担保する（キー挙動自体の不変は model_test のキー操作テストが裏付ける）。
func TestHelpLineTreeContext(t *testing.T) {
	m := newTestModel(t)
	m.focus = focusTree
	view := m.View()
	for _, want := range []string{"switch", "作成", "doctor", "削除", "再起動"} {
		if !strings.Contains(view, want) {
			t.Errorf("focusTree のヘルプ行に %q が無い", want)
		}
	}
}

func TestHelpLineLogContext(t *testing.T) {
	m := newTestModel(t)
	m.focus = focusLog
	view := m.View()
	for _, want := range []string{"追従", "フィルタ", "前世代", "折り返し"} {
		if !strings.Contains(view, want) {
			t.Errorf("focusLog のヘルプ行に %q が無い", want)
		}
	}
}

// doctorMode は右ペインに doctor の結果テキストを表示する。
func TestDoctorModeShowsResult(t *testing.T) {
	m := newTestModel(t)
	m.doctorText = []string{"問題は見つかりませんでした"}
	m.doctorMode = true
	m.rebuildLog()

	view := m.View()
	if !strings.Contains(view, "問題は見つかりませんでした") {
		t.Error("doctor 結果のテキストが表示されていない")
	}
	if !strings.Contains(view, "doctor 結果") {
		t.Error("doctor 結果の見出しが表示されていない")
	}
}
