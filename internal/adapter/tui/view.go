package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// visibleEvents は左ペインのフッターに表示するイベント履歴の行数。フッターを短く保ち、
// ノード一覧の領域を潰さないための上限。
const visibleEvents = 6

// View は現在のモデルを 1 画面に描画する。レイアウトは lazygit / charm 風の 2 ペイン:
// 左（ツリー）と右（ログ／doctor 結果／フォーム）をそれぞれ角丸ボーダーで囲み、見出しは
// 上辺のボーダーに埋め込む。フォーカス中のペインはボーダー色（colorAccent）で示す。各
// ボーダーは左右 2 桁・上下 2 行を消費する。ヘルプ行だけはボーダーの外、画面最下に置く。
// note（一時メッセージ）・実行中表示・イベント履歴は左ペインのボーダー内に収める。
func (m *model) View() string {
	if !m.ready {
		return "起動中…"
	}
	// 左右のボックス総幅と、その内側（ボーダーを除いた）幅・高さ。負値・0 は防御的に
	// 1 へ丸める（超狭小端末で panic させない）。
	leftW := m.leftW()
	rightBoxW := max(1, m.width-leftW)
	leftInner := max(1, leftW-2)
	rightInner := max(1, rightBoxW-2)
	innerH := max(1, m.height-3)

	leftBox := m.renderBox(m.leftTitle(), m.treeLines(innerH), leftInner, innerH, m.focus == focusTree)
	rightBox := m.renderBox(m.logTitle(), m.rightLines(), rightInner, innerH, m.focus == focusLog)

	var b strings.Builder
	for i := range leftBox {
		if i > 0 {
			b.WriteString("\n")
		}
		// 左右のボックスを横に連結し、右端のはみ出しは端末幅に収める。
		b.WriteString(truncLine(leftBox[i]+rightBox[i], m.width))
	}
	b.WriteString("\n")
	// ヘルプ行は文脈別の Binding 列を help.Model が描く（キー=colorAccent 太字・説明=
	// faint・区切り " · "）。ボーダーの外、画面最下に置く。
	b.WriteString(truncLine(m.help.ShortHelpView(m.contextBindings()), m.width))
	return b.String()
}

// renderBox は content（内側の行）を角丸ボーダーで囲み、見出しを上辺へ埋め込んだ
// innerH+2 行を返す。各行の表示幅は innerW+2。フォーカス時はボーダーを colorAccent、
// 非フォーカス時は faint で描く。content が innerH に満たない行は空白で埋め、超えた分は
// 捨てる。
func (m *model) renderBox(title string, content []string, innerW, innerH int, focused bool) []string {
	sty := styBorder
	if focused {
		sty = styBorderFocus
	}
	out := make([]string, 0, innerH+2)
	out = append(out, borderTop(title, innerW, sty))
	bar := sty.Render("│")
	for i := 0; i < innerH; i++ {
		line := ""
		if i < len(content) {
			line = content[i]
		}
		out = append(out, bar+padDisplay(line, innerW)+bar)
	}
	out = append(out, sty.Render("╰"+strings.Repeat("─", innerW)+"╯"))
	return out
}

// borderTop は角丸ボーダーの上辺を組み、見出し title を埋め込む（例:
// "╭─ WORKTREES ────╮"）。title は色付き（ANSI エスケープを含みうる）でよく、幅計算は
// x/ansi ベースのヘルパで行う。狭すぎて見出しが入らないときは素の上辺を返す。ボーダー
// 文字だけを sty で塗り、title 自身の色（見出し色・ピル色）は保つ。
func borderTop(title string, innerW int, sty lipgloss.Style) string {
	// 見出しが無い、または "╭─ x ─╮" の最小構成すら入らないときは素の上辺。
	if title == "" || innerW < 5 {
		return sty.Render("╭" + strings.Repeat("─", innerW) + "╮")
	}
	// "─ " + title + " " + 末尾ダッシュ(>=1) が innerW に収まるよう見出しを切り詰める。
	t := truncDisplay(title, innerW-4)
	rest := innerW - lipgloss.Width(t) - 3
	if rest < 1 {
		return sty.Render("╭" + strings.Repeat("─", innerW) + "╮")
	}
	return sty.Render("╭─ ") + t + sty.Render(" "+strings.Repeat("─", rest)+"╮")
}

// leftTitle は左ペインの見出し。
func (m *model) leftTitle() string {
	return m.paneTitle("WORKTREES", focusTree)
}

// paneTitle はペイン見出しを、フォーカスの有無で色分けして描く（フォーカス=colorAccent+
// 太字、非フォーカス=faint）。ボーダー色と揃えてフォーカスを示す。
func (m *model) paneTitle(label string, f focusID) string {
	if m.focus == f {
		return styPaneTitleFocus.Render(label)
	}
	return styPaneTitle.Render(label)
}

// logTitle は右ペインの見出し: 対象（repo/server @ worktree）と、モードのフラグ
// （追従・前世代・フィルタ／入力中の input.View()）をピルで添える。doctor 結果・
// フォーム表示中は専用の見出し。
func (m *model) logTitle() string {
	if m.form != nil {
		// フォーム表示中は種類ごとの見出しに切り替える。
		label := "入力中"
		switch m.formKind {
		case formCreate:
			label = "worktree 作成"
		case formAlias:
			label = "別名"
		case formRemove:
			label = "削除の確認"
		}
		return m.paneTitle(label, focusLog)
	}
	if m.doctorMode {
		return m.paneTitle("doctor 結果", focusLog)
	}
	label := "ログ"
	if m.curKey != "" {
		if wt, repo, srv, ok := splitKey(m.curKey); ok {
			label = fmt.Sprintf("ログ: %s/%s @ %s", repo, srv, wt)
		}
	}
	title := m.paneTitle(label, focusLog)
	if pills := m.logPills(); pills != "" {
		title += " " + pills
	}
	return title
}

// logPills はログ見出しへ添えるフラグのピル（バッジ）列を組む。文字色のみで表現し
// （背景色はテーマ追従のため使わない）、前世代/フィルタ=シアン・読取失敗=赤。
// フィルタ入力中は textinput の生ビューをそのまま見せる。
// 既定状態（末尾追従）にはバッジを出さない — 常時表示はノイズで、注意が要るのは
// 上へスクロールして追従が切れている間だけなので、そのときだけ黄で示す。
func (m *model) logPills() string {
	var pills []string
	if !m.follow {
		pills = append(pills, styPillWarn.Render("[追従停止]"))
	}
	if m.prev {
		pills = append(pills, styPillAccent.Render("[前世代]"))
	}
	if m.prompt == promptFilter {
		pills = append(pills, m.input.View())
	} else if m.filter != "" {
		pills = append(pills, styPillAccent.Render("[/"+m.filter+"]"))
	}
	if m.curReadErr != "" {
		pills = append(pills, styPillError.Render("[読取失敗]"))
	}
	return strings.Join(pills, " ")
}

// treeLines は左ペインの行を組む: スクロールするノード一覧の下に、実行中表示と直近の
// イベント履歴を固定で置く。
func (m *model) treeLines(h int) []string {
	// 下部に置くフッター（空行 + 状態行 + イベント）を先に組み、残りをノード領域にする。
	// すべて左ペインのボーダー内に収める。状態行は 1 枠で、実行中はスピナー付きの
	// 「実行中: …」を、そうでなければ直近の一時メッセージ（note）を出す（startOp で
	// note はクリアされるため両者は時間的に排他）。
	footer := []string{""}
	switch {
	case m.opRunning:
		// MiniDot のスピナーを colorAccent で先頭に回す（実行中のみ tick を回す）。
		footer = append(footer, m.spin.View()+" "+styNote.Render("実行中: "+m.opLabel+" …"))
	case m.note != "":
		footer = append(footer, m.noteLine())
	}
	if len(m.events) > 0 {
		footer = append(footer, styHelp.Render("── イベント ──"))
		ev := m.events
		if len(ev) > visibleEvents {
			ev = ev[len(ev)-visibleEvents:]
		}
		footer = append(footer, ev...)
	}
	if len(footer) > h {
		footer = footer[:h]
	}
	nodeAreaH := max(1, h-len(footer))

	var nodeLines []string
	switch {
	case m.treesErr != "":
		nodeLines = []string{styErrNote.Render("取得失敗: " + m.treesErr)}
	case m.trees == nil:
		nodeLines = []string{"読み込み中…"}
	case len(m.nodes) == 0:
		nodeLines = []string{"worktree がありません（n で作成）"}
	default:
		nodeLines = make([]string, len(m.nodes))
		for i, n := range m.nodes {
			nodeLines[i] = m.nodeLine(i, n)
		}
	}

	m.adjustTreeTop(len(nodeLines), nodeAreaH)
	visible := nodeLines
	if m.treeTop < len(visible) {
		visible = visible[m.treeTop:]
	} else {
		visible = nil
	}
	if len(visible) > nodeAreaH {
		visible = visible[:nodeAreaH]
	}
	for len(visible) < nodeAreaH {
		visible = append(visible, "")
	}
	return append(visible, footer...)
}

// adjustTreeTop は sel が可視域（treeTop..treeTop+viewH）に入るようスクロール位置を
// 詰める。ノード数が領域に満たなければ先頭に固定する。
func (m *model) adjustTreeTop(total, viewH int) {
	if m.sel < m.treeTop {
		m.treeTop = m.sel
	}
	if m.sel >= m.treeTop+viewH {
		m.treeTop = m.sel - viewH + 1
	}
	m.treeTop = clamp(m.treeTop, 0, max(0, total-viewH))
}

// nodeLine は 1 ノードを描く。選択行は反転をやめ、行頭に colorAccent の ▌ インジケータを
// 立てて本文を太字にする（非選択行は行頭 1 桁の空白で整列を保つ）。状態マークは選択・
// 非選択のいずれも状態色を保つ（インジケータ方式なのでマーク色と共存できる）。
func (m *model) nodeLine(i int, n node) string {
	sel := i == m.sel
	// 行頭 1 桁: 選択は ▌（accent）、非選択は空白で整列。
	ind := " "
	if sel {
		ind = stySelIndicator.Render("▌")
	}

	if n.isWorktree() {
		// 展開中は ▾、折りたたみ中は ▸。折りたたみは配下サーバーが見えないため、状態を
		// 集約したマーク（例: ●2 ✗1）を見出しの右に添える。
		glyph := "▾"
		if n.collapsed {
			glyph = "▸"
		}
		name := glyph + " " + n.wt
		if n.alias != "" {
			name += " (" + n.alias + ")"
		}
		if n.broken {
			name += " (!)"
		}
		if sel {
			// 見出し本文だけ太字にし、集約マークは状態色を保つ（太字で色を潰さない）。
			name = stySelText.Render(name)
		}
		if agg := aggColored(n); n.collapsed && agg != "" {
			name += "  " + agg
		}
		return ind + name
	}

	suffix := n.repo + "/" + n.server
	if n.running && n.pid != 0 {
		suffix += " :" + strconv.Itoa(n.pid)
	}
	if sel {
		suffix = stySelText.Render(suffix)
	}
	// サーバーノードは見出しの下に 2 桁インデントして並べる（マークは状態色）。
	return ind + "  " + markColored(n) + " " + suffix
}

// aggColored は集約マークを状態色付きで組む。色は状態マークの既存定数を
// 流用: 稼働=緑・クラッシュ=赤・停止=faint。
func aggColored(n node) string {
	var parts []string
	if n.nRunning > 0 {
		parts = append(parts, styMarkRunning.Render("●"+strconv.Itoa(n.nRunning)))
	}
	if n.nCrashed > 0 {
		parts = append(parts, styMarkCrashed.Render("✗"+strconv.Itoa(n.nCrashed)))
	}
	if n.nStopped > 0 {
		parts = append(parts, styMarkStopped.Render("○"+strconv.Itoa(n.nStopped)))
	}
	return strings.Join(parts, " ")
}

// markGlyph は無色のマーク記号（選択行用）。
func markGlyph(n node) string {
	switch {
	case n.running:
		return "●"
	case n.crashed:
		return "✗"
	default:
		return "○"
	}
}

// markColored は色付きのマーク記号（非選択行用）。
func markColored(n node) string {
	switch {
	case n.running:
		return styMarkRunning.Render("●")
	case n.crashed:
		return styMarkCrashed.Render("✗")
	default:
		return styMarkStopped.Render("○")
	}
}

// rightLines は右ペインの行を返す。huh フォーム表示中はフォームを、それ以外は
// ビューポート（ログ／doctor 結果）を描く。フォームは通常時のみ開くため doctor
// 結果より優先してよい。
func (m *model) rightLines() []string {
	if m.form != nil {
		return strings.Split(m.form.View(), "\n")
	}
	return strings.Split(m.vp.View(), "\n")
}

// noteLine はフッターの一時メッセージ行（左ペインのボーダー内に置く）。
func (m *model) noteLine() string {
	if m.note == "" {
		return ""
	}
	if m.noteErr {
		return styErrNote.Render(m.note)
	}
	return styNote.Render(m.note)
}

// truncLine は 1 行を表示幅 w に収める。
func truncLine(s string, w int) string {
	if w <= 0 {
		return s
	}
	return truncDisplay(s, w)
}
