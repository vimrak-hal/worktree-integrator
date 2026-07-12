package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/vimrak-hal/worktree-integrator/internal/app/tree"
)

// 左カラムは WORKTREES とイベントの 2 ボックスが縦積みになり、右カラムはログの全高
// ボックス。見出しは各上辺に埋め込まれる。左にツリーのノード名、右にログ行が同時に
// 描かれる。選択行には ▌ インジケータが立つ。
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
	for _, want := range []string{
		"feat-a",         // ツリーの worktree 見出し
		"api/backend",    // 右ペインのログ見出しに埋め込まれた対象
		"hello-log-line", // ビューポートのログ本文
		"WORKTREES",      // 左カラム上ボックスの見出し（上辺に埋め込み）
		"イベント",           // 左カラム下ボックスの見出し（上辺に埋め込み）
		"╭",              // 角丸ボーダーの上辺
		"╰",              // 角丸ボーダーの下辺
		"│",              // ボーダーの側辺
		"▌",              // 選択行のインジケータ（sel=0 の feat-a 見出し）
	} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q", want)
		}
	}
}

// イベントが発生すると左カラム下のイベントボックスへ行が出る。状態行（note）も
// イベントボックスの 1 行目に出る。
func TestEventBoxShowsEvents(t *testing.T) {
	m := newTestModel(t)
	m.cfg = serverCfg()
	m.trees = treesResult(tree.WorktreeRow{Name: "feat-a", Repos: []tree.RepoCell{{Repo: "api"}}})
	m.buildNodes()
	m.events = []string{"created feat-a", "switched to backend"}
	m.note = "作成しました"

	view := m.View()
	lines := strings.Split(view, "\n")
	evHead := -1
	for i, ln := range lines {
		if strings.Contains(ln, "イベント") {
			evHead = i
			break
		}
	}
	if evHead < 0 {
		t.Fatal("イベントボックスの見出しが無い")
	}
	// 状態行とイベント行はイベントボックスの見出しより後の行に出る（下ボックス内）。
	for _, want := range []string{"作成しました", "created feat-a", "switched to backend"} {
		found := false
		for _, ln := range lines[evHead+1:] {
			if strings.Contains(ln, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("イベントボックス内に %q が出ていない", want)
		}
	}
}

// 端末が低いときはイベントボックスを出さず、従来どおりツリーボックスのみにする（状態行は
// ツリー最下行へ退避、イベントは非表示）。この間もツリーの選択・スクロールは機能し panic
// しない。
func TestLowTerminalHidesEventBox(t *testing.T) {
	m := newTestModel(t)
	m.cfg = serverCfg()
	m.trees = treesResult(tree.WorktreeRow{Name: "feat-a", Repos: []tree.RepoCell{{Repo: "api"}},
		Servers: []tree.ServerCell{{Repo: "api", Server: "backend", Pid: 4242}}})
	m.buildNodes()
	m.events = []string{"created feat-a"}
	m.note = "作成しました"
	// イベントボックス + ツリー最低 3 行を確保できない低い端末。
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 10})

	view := m.View()
	if strings.Contains(view, "イベント") {
		t.Error("低い端末ではイベントボックスの見出しを出さない")
	}
	if strings.Contains(view, "created feat-a") {
		t.Error("低い端末ではイベントを表示しない")
	}
	if !strings.Contains(view, "WORKTREES") {
		t.Error("低い端末でもツリーボックスは出る")
	}
	if !strings.Contains(view, "feat-a") {
		t.Error("低い端末でもツリーノードが機能する")
	}
	// 状態行はツリーボックス最下行へ退避する。
	if !strings.Contains(view, "作成しました") {
		t.Error("退避時、状態行がツリーボックスに出ていない")
	}
	// 選択移動しても panic せず非空を返す。
	m.Update(key("j"))
	if m.View() == "" {
		t.Error("低い端末で選択移動後の View が空")
	}
}

// フォーカス廃止後、全ペインの枠・見出しは常に色 8（明るい黒＝グレー）で描く（faint(SGR 2)
// ではない）。一方、フローティングダイアログの枠はアクセント色（シアン＝色 6）で周囲から
// 浮かせる。faint は減光しない端末が多く差が見えないため、明示的な色にした回帰テスト。
func TestBorderColorsGreyPanesAccentDialog(t *testing.T) {
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	defer lipgloss.SetColorProfile(old)

	border := styBorder.Render("│")
	if !strings.Contains(border, "\x1b[90m") {
		t.Errorf("ペインのボーダーは色 8（\\x1b[90m）で描くべき: %q", border)
	}
	if strings.Contains(border, "\x1b[2m") {
		t.Errorf("ペインのボーダーに faint（SGR 2）を使ってはいけない: %q", border)
	}
	title := styPaneTitle.Render("イベント")
	if !strings.Contains(title, "\x1b[90m") {
		t.Errorf("ペイン見出しは色 8（\\x1b[90m）で描くべき: %q", title)
	}
	if strings.Contains(title, "\x1b[2m") {
		t.Errorf("ペイン見出しに faint（SGR 2）を使ってはいけない: %q", title)
	}
	// ダイアログの枠・見出しはアクセント色（色 6＝\x1b[36m）。
	dialogBorder := styDialogBorder.Render("│")
	if !strings.Contains(dialogBorder, "\x1b[36m") {
		t.Errorf("ダイアログのボーダーはアクセント色（\\x1b[36m）で描くべき: %q", dialogBorder)
	}
}

// ログ見出しのフラグはピル（バッジ）として描かれる。既定状態（末尾追従）はバッジを
// 出さず、追従が切れている間だけ [追従停止] を出す。フォーカスは反転ではなく
// ボーダー／見出しの色で表現するため、View に反転（\x1b[7m）を含めない。
func TestViewLogPillsAndNoReverse(t *testing.T) {
	m := newTestModel(t)
	m.cfg = serverCfg()
	m.trees = treesResult(tree.WorktreeRow{Name: "feat-a", Repos: []tree.RepoCell{{Repo: "api"}}})
	m.buildNodes()
	m.curKey = "feat-a\x00api/backend"
	m.bufs[m.curKey] = newRing(10)
	m.follow = true
	m.prev = true
	m.rebuildLog()

	view := m.View()
	if strings.Contains(view, "追従停止") {
		t.Error("追従中（既定状態）に [追従停止] が出ている")
	}
	if !strings.Contains(view, "[前世代]") {
		t.Error("ログ見出しにピル [前世代] が無い")
	}
	if strings.Contains(view, "\x1b[7m") {
		t.Error("リデザイン後の View は反転（\\x1b[7m）を使わない")
	}

	m.follow = false
	view = m.View()
	if !strings.Contains(view, "[追従停止]") {
		t.Error("追従を解除しても [追従停止] が出ない")
	}
}

// 超狭小端末（幅 20×高さ 5 など）でも View は panic せず、非空の文字列を返す。負の
// repeat・範囲外スライスを防いでいることの回帰テスト。
func TestViewTinyTerminalNoPanic(t *testing.T) {
	for _, sz := range []struct{ w, h int }{
		{20, 5}, {1, 1}, {2, 3}, {10, 2}, {40, 1},
	} {
		m := newTestModel(t)
		m.cfg = serverCfg()
		m.trees = treesResult(tree.WorktreeRow{Name: "feat-a", Repos: []tree.RepoCell{{Repo: "api"}},
			Servers: []tree.ServerCell{{Repo: "api", Server: "backend", Pid: 4242}}})
		m.buildNodes()
		m.curKey = "feat-a\x00api/backend"
		m.bufs[m.curKey] = newRing(10)
		m.bufs[m.curKey].push("some-log-line")
		m.opRunning = true // フッターのスピナー行も含めて描く
		m.note = "とても長い一時メッセージが狭い左ペインでも panic しないこと"
		m.Update(tea.WindowSizeMsg{Width: sz.w, Height: sz.h})
		if got := m.View(); got == "" {
			t.Errorf("View(%dx%d) は非空を返すべき", sz.w, sz.h)
		}
		// ダイアログが画面より大きくなりうる狭小端末でも、オーバーレイ合成が panic しない。
		m.formAlias = ""
		m.form = newAliasForm(&m.formAlias, m.dialogInnerW())
		m.formKind = formAlias
		if got := m.View(); got == "" {
			t.Errorf("フォームダイアログ表示中の View(%dx%d) は非空を返すべき", sz.w, sz.h)
		}
		m.form, m.formKind = nil, formNone
		m.Update(opDoneMsg{summary: "doctor", doctorText: []string{"line-a", "line-b"}})
		if got := m.View(); got == "" {
			t.Errorf("doctor ダイアログ表示中の View(%dx%d) は非空を返すべき", sz.w, sz.h)
		}
	}
}

// 折りたたみ見出しは ▸ グリフと、配下サーバーの状態を集約したマーク（0 件は非表示）を
// 右に付ける。選択行は行頭に ▌ インジケータを立てて本文を太字にし、非選択行は行頭 1 桁の
// 空白で整列する。集約マークは選択・非選択のどちらも状態色を保つ（数え上げは同じ）。
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

	// 選択行: 行頭に ▌ インジケータ、▸ と 稼働1・停止1 の集約。0 件のクラッシュは出ない。
	m.sel = 0
	sel := m.nodeLine(0, m.nodes[0])
	for _, want := range []string{"▌", "▸", "feat-x", "●1", "○1"} {
		if !strings.Contains(sel, want) {
			t.Errorf("選択折りたたみ見出しに %q が無い: %q", want, sel)
		}
	}
	if strings.Contains(sel, "✗") {
		t.Errorf("0 件のクラッシュマークを出してはいけない: %q", sel)
	}

	// 非選択行にはインジケータが立たず、同じ集約が状態色で出る。
	m.sel = -1
	colored := m.nodeLine(0, m.nodes[0])
	for _, want := range []string{"▸", "●1", "○1"} {
		if !strings.Contains(colored, want) {
			t.Errorf("非選択折りたたみ見出しに %q が無い: %q", want, colored)
		}
	}
	if strings.Contains(colored, "▌") {
		t.Errorf("非選択行にインジケータ ▌ を出してはいけない: %q", colored)
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
func TestHelpLineMergedContext(t *testing.T) {
	m := newTestModel(t)
	view := m.View()
	// フォーカス廃止後、ツリーのキーとグローバル化したログのキーが同じ 1 行に共存する
	// （前方の主要項目は幅 120 で省略されない）。
	for _, want := range []string{"switch", "作成", "削除", "doctor", "追従", "フィルタ", "前世代"} {
		if !strings.Contains(view, want) {
			t.Errorf("通常時のヘルプ行に %q が無い", want)
		}
	}
}

// doctorMode は中央のフローティングダイアログに doctor の結果テキストを表示し、背後の
// 右ペイン（ログ）はそのまま残る。ダイアログには見出しとアクセント色のボーダーが付く。
func TestDoctorDialogShowsResult(t *testing.T) {
	m := newTestModel(t)
	m.cfg = serverCfg()
	m.trees = treesResult(tree.WorktreeRow{Name: "feat-a", Repos: []tree.RepoCell{{Repo: "api"}}})
	m.buildNodes()
	m.curKey = "feat-a\x00api/backend"
	m.bufs[m.curKey] = newRing(10)
	m.bufs[m.curKey].push("hello-log-line")
	m.rebuildLog()
	// doctor 結果へ遷移（ダイアログ用ビューポートを組む）。
	m.Update(opDoneMsg{summary: "doctor 完了", doctorText: []string{"問題は見つかりませんでした"}})

	view := m.View()
	if !strings.Contains(view, "問題は見つかりませんでした") {
		t.Error("doctor 結果のテキストが表示されていない")
	}
	if !strings.Contains(view, "doctor 結果") {
		t.Error("doctor 結果ダイアログの見出しが表示されていない")
	}
	// ダイアログは中央のため、背後の右ペイン（ログ）のログ本文は上下いずれかに残る。
	if !strings.Contains(view, "hello-log-line") {
		t.Error("doctor ダイアログの背後に残るログ本文が消えている")
	}
}

// フォーム・doctor はフローティングダイアログとして中央へ重ねられる: ダイアログの見出し・
// 中身が中央付近の行に現れ、背後のベース画面（WORKTREES / 最下のヘルプ行）が上下に残る。
func TestDialogOverlayCentered(t *testing.T) {
	m := newTestModel(t)
	m.cfg = serverCfg()
	m.trees = treesResult(tree.WorktreeRow{Name: "feat-a", Repos: []tree.RepoCell{{Repo: "api"}}})
	m.buildNodes()
	m.curKey = "feat-a\x00api/backend"
	m.bufs[m.curKey] = newRing(10)
	m.rebuildLog()

	// 別名フォームを中央ダイアログとして開く（Init でフィールドが描画される）。
	m.formAlias = "ログイン画面"
	m.form = newAliasForm(&m.formAlias, m.dialogInnerW())
	m.formKind = formAlias
	m.form.Init()

	view := m.View()
	lines := strings.Split(view, "\n")
	if !strings.Contains(view, "別名") {
		t.Error("フォームダイアログの見出し「別名」が出ていない")
	}
	// 背後のベース画面が上部に残る（上端付近の行に WORKTREES 見出しが見える）。
	topHasBase := false
	for _, ln := range lines[:3] {
		if strings.Contains(ln, "WORKTREES") {
			topHasBase = true
			break
		}
	}
	if !topHasBase {
		t.Error("ダイアログの上にベース画面（WORKTREES）が残っていない")
	}
	// 最下のヘルプ行はダイアログの外（ベース画面）に残る。フォーム表示中はフォーム文脈の
	// ヘルプ（Esc 中止 など）が出る。
	if !strings.Contains(lines[len(lines)-1], "中止") {
		t.Errorf("最下のヘルプ行がダイアログに潰されている: %q", lines[len(lines)-1])
	}
	// フォームの中身（プリフィルされた別名）がダイアログ内に描かれる。
	if !strings.Contains(view, "ログイン画面") {
		t.Error("フォームの中身がダイアログに描かれていない")
	}
}
