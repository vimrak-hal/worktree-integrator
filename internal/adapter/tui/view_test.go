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

// 折りたたみ見出しは ▸ グリフと、配下サーバーの状態を集約したマーク（0 件は非表示）を
// 右に付ける。選択行は無色、非選択行は状態色付きで組む（どちらも数え上げは同じ）。
func TestCollapsedHeadingAggregateMarks(t *testing.T) {
	m := newTestModel(t)
	m.cfg = serverCfg()
	// backend 稼働 + web 停止（クラッシュ 0）。明示的に折りたたむ。
	m.collapsed = map[string]bool{"feat-x": true}
	m.trees = treesResult(
		tree.WorktreeRow{Name: "feat-x", Repos: []tree.RepoCell{{Repo: "api"}},
			Servers: []tree.ServerCell{{Repo: "api", Server: "backend", Pid: 4242}}},
	)
	m.buildNodes()
	if len(m.nodes) != 1 || !m.nodes[0].collapsed {
		t.Fatalf("明示折りたたみで見出しのみになるべき: %+v", m.nodes)
	}

	// 選択行（無色・反転）: ▸ と 稼働1・停止1 の集約。0 件のクラッシュは出ない。
	m.sel = 0
	sel := m.nodeLine(0, m.nodes[0])
	for _, want := range []string{"▸", "feat-x", "●1", "○1"} {
		if !strings.Contains(sel, want) {
			t.Errorf("選択折りたたみ見出しに %q が無い: %q", want, sel)
		}
	}
	if strings.Contains(sel, "✗") {
		t.Errorf("0 件のクラッシュマークを出してはいけない: %q", sel)
	}

	// 非選択行（色付き）でも同じ集約が出る。
	m.sel = -1
	colored := m.nodeLine(0, m.nodes[0])
	for _, want := range []string{"▸", "●1", "○1"} {
		if !strings.Contains(colored, want) {
			t.Errorf("非選択折りたたみ見出しに %q が無い: %q", want, colored)
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
