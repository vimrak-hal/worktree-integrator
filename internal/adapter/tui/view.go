package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// visibleEvents はイベントボックスに表示するイベント履歴の行数。
const visibleEvents = 6

// eventBoxInnerH はイベントボックスの内側高さ（固定）。1 行目の状態行 + 直近イベント
// visibleEvents 行。固定にすることでイベント件数が増減してもレイアウトが揺れない。
const eventBoxInnerH = 1 + visibleEvents

// View は現在のモデルを 1 画面に描画する。レイアウトは lazygit / charm 風の 2 カラム:
// 左カラムは上「WORKTREES」（ツリーノード）+ 下「イベント」の 2 ボックスを縦積みにし、
// 右カラム（ログ）は全高 1 ボックス。それぞれ角丸ボーダーで囲み、見出しは上辺のボーダーに
// 埋め込む。フォーカスの概念は無いため全ボックスのボーダー・見出しは常にグレー（colorMuted）
// で、いま何に効くかはツリーの ▌ カーソルとダイアログの存在で示す。フォーム・doctor は
// 画面中央のフローティングダイアログとしてベース画面へ ANSI セーフに重ねる。各ボーダーは
// 左右 2 桁・上下 2 行を消費する。ヘルプ行だけはボーダーの外、画面最下に置く。
func (m *model) View() string {
	if !m.ready {
		return "起動中…"
	}
	base := m.baseLines()
	// フォーム・doctor は中央のフローティングダイアログとしてベース画面へ重ねる（form は
	// 通常時のみ開くため doctor より優先）。
	switch {
	case m.form != nil:
		base = overlayCentered(base, m.formDialog(), m.width)
	case m.doctorMode:
		base = overlayCentered(base, m.doctorDialog(), m.width)
	}
	return strings.Join(base, "\n")
}

// baseLines はダイアログを重ねる前のベース画面を行の並びで返す（本文 + 最下のヘルプ行）。
func (m *model) baseLines() []string {
	// 左右のボックス総幅と、その内側（ボーダーを除いた）幅・高さ。負値・0 は防御的に
	// 1 へ丸める（超狭小端末で panic させない）。
	leftW := m.leftW()
	rightBoxW := max(1, m.width-leftW)
	leftInner := max(1, leftW-2)
	rightInner := max(1, rightBoxW-2)
	innerH := max(1, m.height-3)

	leftBox := m.leftColumn(leftInner, innerH)
	// 右ペインは常にログ（doctor は別ダイアログ、form も別ダイアログ）。
	rightBox := m.renderBox(m.logTitle(), m.rightLines(), rightInner, innerH)

	out := make([]string, 0, len(leftBox)+1)
	for i := range leftBox {
		// 左右のボックスを横に連結し、右端のはみ出しは端末幅に収める。
		out = append(out, truncLine(leftBox[i]+rightBox[i], m.width))
	}
	// ヘルプ行は文脈別の Binding 列を help.Model が描く（キー=colorAccent 太字・説明=
	// faint・区切り " · "）。ボーダーの外、画面最下に置く。
	out = append(out, truncLine(m.help.ShortHelpView(m.contextBindings()), m.width))
	return out
}

// renderBox は content（内側の行）を角丸ボーダー（常にグレー）で囲み、見出しを上辺へ
// 埋め込んだ innerH+2 行を返す。各行の表示幅は innerW+2。content が innerH に満たない
// 行は空白で埋め、超えた分は捨てる。
func (m *model) renderBox(title string, content []string, innerW, innerH int) []string {
	sty := styBorder
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

// leftTitle は左ペインの見出し（常にグレー）。
func (m *model) leftTitle() string {
	return styPaneTitle.Render("WORKTREES")
}

// logTitle は右ペインの見出し（常にグレー）: 対象（repo/server @ worktree）と、モードの
// フラグ（追従・前世代・フィルタ／入力中の input.View()）をピルで添える。フォーム・doctor は
// 別のフローティングダイアログで描くため、この見出しは常にログの見出しになる。
func (m *model) logTitle() string {
	label := "ログ"
	if m.curKey != "" {
		if wt, repo, srv, ok := splitKey(m.curKey); ok {
			label = fmt.Sprintf("ログ: %s/%s @ %s", repo, srv, wt)
		}
	}
	title := styPaneTitle.Render(label)
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

// leftColumn は左カラム（WORKTREES + イベント の 2 ボックスを縦積み）を組み、右ボックスと
// 行数を揃えるため innerH+2 行を返す。イベントボックスは内側高さ固定（eventBoxInnerH）で、
// 残りがツリーボックスになる。端末が低くツリー本体に最低 3 行を確保できないときは退避して
// イベントボックスを出さず、従来どおりツリーボックスのみ（状態行はツリー最下行へ、イベント
// は非表示）にする。ボーダー・見出しはフォーカス廃止により常にグレー。
func (m *model) leftColumn(innerW, innerH int) []string {
	// 左カラム全体は右ボックスと同じ innerH+2 行を占める。イベントボックス（上下ボーダー
	// 込みで eventBoxInnerH+2 行）を引いた残りがツリーボックスの領域。
	treeInnerH := innerH - eventBoxInnerH - 2
	// 退避: イベントボックス + ツリー最低 3 行を確保できないときはツリーボックスのみ。
	if treeInnerH < 3 {
		return m.renderBox(m.leftTitle(), m.treeOnlyLines(innerH), innerW, innerH)
	}
	treeBox := m.renderBox(m.leftTitle(), m.visibleNodeLines(treeInnerH), innerW, treeInnerH)
	eventBox := m.renderBox(m.eventTitle(), m.eventLines(), innerW, eventBoxInnerH)
	return append(treeBox, eventBox...)
}

// eventTitle はイベントボックスの見出し（常にグレー）。
func (m *model) eventTitle() string {
	return styPaneTitle.Render("イベント")
}

// statusLine は状態行を返す: 実行中はスピナー付きの「実行中: …」を、そうでなければ直近の
// 一時メッセージ（note）を出す（startOp で note はクリアされるため両者は時間的に排他）。
// どちらも無ければ空文字。
func (m *model) statusLine() string {
	switch {
	case m.opRunning:
		// MiniDot のスピナーを colorAccent で先頭に回す（実行中のみ tick を回す）。
		return m.spin.View() + " " + styNote.Render("実行中: "+m.opLabel+" …")
	case m.note != "":
		return m.noteLine()
	default:
		return ""
	}
}

// eventLines はイベントボックスの内側（1 + visibleEvents 行）を組む: 1 行目に状態行、続けて
// 直近のイベント（新しいものが下）。不足行は renderBox が空白で埋めるためここでは詰めない。
func (m *model) eventLines() []string {
	out := []string{m.statusLine()}
	ev := m.events
	if len(ev) > visibleEvents {
		ev = ev[len(ev)-visibleEvents:]
	}
	return append(out, ev...)
}

// treeOnlyLines は退避時のツリーボックス内側を h 行で返す: ノードで埋め、状態行があれば
// 最下行に置く（イベントは非表示）。
func (m *model) treeOnlyLines(h int) []string {
	status := m.statusLine()
	if status == "" {
		return m.visibleNodeLines(h)
	}
	return append(m.visibleNodeLines(max(1, h-1)), status)
}

// visibleNodeLines はツリーのノード行を組み、選択行が可視域に入るようスクロールして
// area 行ぶんへ切り詰め・空白パディングして返す（フッター合成は含まない）。
func (m *model) visibleNodeLines(area int) []string {
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

	m.adjustTreeTop(len(nodeLines), area)
	visible := nodeLines
	if m.treeTop < len(visible) {
		visible = visible[m.treeTop:]
	} else {
		visible = nil
	}
	if len(visible) > area {
		visible = visible[:area]
	}
	for len(visible) < area {
		visible = append(visible, "")
	}
	return visible
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

// rightLines は右ペインの行（常にログのビューポート）を返す。フォーム・doctor は中央の
// フローティングダイアログで描くため、右ペインは常にログを出す。
func (m *model) rightLines() []string {
	return strings.Split(m.vp.View(), "\n")
}

// formDialog はフォーム（作成・別名・削除確認）を中央フローティングダイアログの行の並びで
// 組む。角丸ボーダー（colorAccent）+ 上辺にアクセント太字の種類見出し。高さは huh フォームの
// 内容に合わせるため、フォーム描画の末尾の空行を落としてから箱に収める。
func (m *model) formDialog() []string {
	label := "入力中"
	switch m.formKind {
	case formCreate:
		label = "worktree 作成"
	case formAlias:
		label = "別名"
	case formRemove:
		label = "削除の確認"
	}
	content := trimTrailingBlank(strings.Split(m.form.View(), "\n"))
	// 天井: 画面高から溢れないよう内容を切り詰める（ダイアログ + 上下ボーダーで height 未満に）。
	if cap := max(1, m.height-4); len(content) > cap {
		content = content[:cap]
	}
	return renderDialogBox(styDialogTitle.Render(label), content, m.dialogInnerW())
}

// doctorDialog は doctor 結果を中央の大きめフローティングダイアログの行の並びで組む。内容は
// 専用ビューポート（dvp）から取り、スクロール位置はキー操作で dvp が保持する。
func (m *model) doctorDialog() []string {
	content := strings.Split(m.dvp.View(), "\n")
	return renderDialogBox(styDialogTitle.Render("doctor 結果"), content, m.doctorInnerW())
}

// renderDialogBox は content をアクセント色の角丸ボーダーで囲み、上辺へ見出し title（描画
// 済み）を埋め込んだ行の並びを返す。renderBox と違い高さは content に合わせ、固定行数の
// パディングはしない（ダイアログは内容ぴったりに収める）。
func renderDialogBox(title string, content []string, innerW int) []string {
	sty := styDialogBorder
	out := make([]string, 0, len(content)+2)
	out = append(out, borderTop(title, innerW, sty))
	bar := sty.Render("│")
	for _, line := range content {
		out = append(out, bar+padDisplay(line, innerW)+bar)
	}
	out = append(out, sty.Render("╰"+strings.Repeat("─", innerW)+"╯"))
	return out
}

// overlayCentered はダイアログ dialog をベース画面 base の中央へ ANSI セーフに重ねる。各
// ダイアログ行について、対応するベース行を「左部分 + ダイアログ行 + 右部分」へ x/ansi の
// Truncate（左を桁数で切る）と TruncateLeft（左 x+幅 桁を落として右を得る）で接合する。
// 接合部ではスタイルのにじみを防ぐため \x1b[0m でリセットしてから各部を書く。ダイアログが
// 画面より大きい・端に寄る場合も範囲外行は捨てて panic させない。
func overlayCentered(base, dialog []string, width int) []string {
	if len(dialog) == 0 {
		return base
	}
	dw := 0
	for _, dl := range dialog {
		if w := lipgloss.Width(dl); w > dw {
			dw = w
		}
	}
	x := max(0, (width-dw)/2)
	y := max(0, (len(base)-len(dialog))/2)
	out := make([]string, len(base))
	copy(out, base)
	for i, dl := range dialog {
		row := y + i
		if row < 0 || row >= len(out) {
			continue
		}
		out[row] = spliceLine(out[row], dl, x, dw)
	}
	return out
}

// spliceLine はベース行 base の桁 [x, x+dw) をダイアログ行 dl で置き換えた 1 行を返す。左
// 部分はちょうど x 桁へパディングし、右部分は base の x+dw 桁以降。接合部は \x1b[0m で
// リセットしてからつなぎ、左のベース色が dl へ、dl の色が右のベースへにじむのを防ぐ。
func spliceLine(base, dl string, x, dw int) string {
	left := padDisplay(ansi.Truncate(base, x, ""), x)
	right := ansi.TruncateLeft(base, x+dw, "")
	return left + "\x1b[0m" + dl + "\x1b[0m" + right
}

// trimTrailingBlank は末尾の空行（表示可能な文字を持たない行）を落とす。huh フォームが
// WithHeight ぶんの空行でパディングするため、ダイアログを内容ぴったりに収める前処理に使う。
func trimTrailingBlank(lines []string) []string {
	end := len(lines)
	for end > 0 && strings.TrimSpace(ansi.Strip(lines[end-1])) == "" {
		end--
	}
	if end == 0 {
		// 全行が空でも 1 行は残す（空のダイアログでも箱を描けるように）。
		return []string{""}
	}
	return lines[:end]
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
