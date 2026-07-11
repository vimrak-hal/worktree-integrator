package tui

import (
	"fmt"
	"strconv"
	"strings"
)

// visibleEvents は左ペインのフッターに表示するイベント履歴の行数。フッターを短く保ち、
// ノード一覧の領域を潰さないための上限。
const visibleEvents = 6

// View は現在のモデルを 1 画面に描画する。レイアウトは lazygit 風の 2 ペイン: 固定 3 行の
// クローム（ペインタイトル行・note 行・ヘルプ行）と、その間の本文（左=ツリー、右=ログ／
// doctor 結果／作成モーダル）を "│" で縦に区切る。本文の高さはビューポートと同じ
// height-3 に揃える。
func (m *model) View() string {
	if !m.ready {
		return "起動中…"
	}
	lw := m.leftW()
	bodyH := max(1, m.height-3)

	left := m.treeLines(bodyH)
	right := m.rightLines()
	body := joinPanes(left, right, lw, bodyH)
	for i := range body {
		// 右端のはみ出しで行が折れないよう全体行を端末幅に収める。
		body[i] = truncLine(body[i], m.width)
	}

	var b strings.Builder
	b.WriteString(truncLine(m.titleLine(lw), m.width))
	b.WriteString("\n")
	b.WriteString(strings.Join(body, "\n"))
	b.WriteString("\n")
	b.WriteString(truncLine(m.noteLine(), m.width))
	b.WriteString("\n")
	// ヘルプ行は文脈別の Binding 列を help.Model が描く（スタイルは newHelp で
	// styHelp 相当の faint・2 スペース区切りに揃えてある）。
	b.WriteString(truncLine(m.help.ShortHelpView(m.contextBindings()), m.width))
	return b.String()
}

// titleLine はペインタイトル行（左=WORKTREES、右=ログ対象とフラグ）。フォーカス側が
// 反転で強調される。
func (m *model) titleLine(lw int) string {
	left := padDisplay(m.paneTitle(" WORKTREES ", focusTree), lw)
	return left + "│" + m.logTitle()
}

// paneTitle はペイン見出しを、フォーカスの有無で色分けして描く。
func (m *model) paneTitle(label string, f focusID) string {
	if m.focus == f {
		return styPaneTitleFocus.Render(label)
	}
	return styPaneTitle.Render(label)
}

// logTitle は右ペインの見出し: 対象（repo/server @ worktree）と、モードのフラグ
// （追従・前世代・フィルタ／入力中の input.View()）。doctor 結果表示中は専用の見出し。
func (m *model) logTitle() string {
	if m.form != nil {
		// フォーム表示中は種類ごとの見出しに切り替える。
		label := " 入力中 "
		switch m.formKind {
		case formCreate:
			label = " worktree 作成 "
		case formAlias:
			label = " 別名 "
		case formRemove:
			label = " 削除の確認 "
		}
		return m.paneTitle(label, focusLog)
	}
	if m.doctorMode {
		return m.paneTitle(" doctor 結果 ", focusLog)
	}
	label := " ログ "
	if m.curKey != "" {
		if wt, repo, srv, ok := splitKey(m.curKey); ok {
			label = fmt.Sprintf(" ログ: %s/%s @ %s ", repo, srv, wt)
		}
	}
	title := m.paneTitle(label, focusLog)

	var flags []string
	if m.follow {
		flags = append(flags, "追従")
	}
	if m.prev {
		flags = append(flags, "前世代(.prev)")
	}
	if m.prompt == promptFilter {
		flags = append(flags, m.input.View())
	} else if m.filter != "" {
		flags = append(flags, "/"+m.filter)
	}
	if m.curReadErr != "" {
		flags = append(flags, "読取失敗")
	}
	if len(flags) > 0 {
		title += " " + styFlag.Render(strings.Join(flags, " "))
	}
	return title
}

// treeLines は左ペインの行を組む: スクロールするノード一覧の下に、実行中表示と直近の
// イベント履歴を固定で置く。
func (m *model) treeLines(h int) []string {
	// 下部に置くフッター（空行 + 実行中 + イベント）を先に組み、残りをノード領域にする。
	footer := []string{""}
	if m.opRunning {
		footer = append(footer, styNote.Render("実行中: "+m.opLabel+" …"))
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

// nodeLine は 1 ノードを描く。選択行は色付けを諦めてプレーン文字列を組んでから全体を
// 反転する（マークの色との共存を避ける）。非選択のサーバーノードはマークを色付けする。
func (m *model) nodeLine(i int, n node) string {
	if n.isWorktree() {
		// 展開中は ▾、折りたたみ中は ▸。折りたたみは配下サーバーが見えないため、状態を
		// 集約したマーク（例: ●2 ✗1）を見出しの右に添える。
		glyph := "▾"
		if n.collapsed {
			glyph = "▸"
		}
		text := glyph + " " + n.wt
		if n.alias != "" {
			text += " (" + n.alias + ")"
		}
		if n.broken {
			text += " (!)"
		}
		if i == m.sel {
			// 選択行はマークの色と反転の共存を避け、無色で組んでから全体を反転する。
			if agg := aggGlyphs(n); n.collapsed && agg != "" {
				text += "  " + agg
			}
			return stySelected.Render(text)
		}
		if agg := aggColored(n); n.collapsed && agg != "" {
			text += "  " + agg
		}
		return text
	}

	suffix := n.repo + "/" + n.server
	if n.running && n.pid != 0 {
		suffix += " :" + strconv.Itoa(n.pid)
	}
	if i == m.sel {
		return stySelected.Render("  " + markGlyph(n) + " " + suffix)
	}
	return "  " + markColored(n) + " " + suffix
}

// aggGlyphs は折りたたみ見出しの集約マーク（無色・選択行用）。並びは 稼働(●)→
// クラッシュ(✗)→停止(○)、0 件の状態は表示しない。
func aggGlyphs(n node) string {
	var parts []string
	if n.nRunning > 0 {
		parts = append(parts, "●"+strconv.Itoa(n.nRunning))
	}
	if n.nCrashed > 0 {
		parts = append(parts, "✗"+strconv.Itoa(n.nCrashed))
	}
	if n.nStopped > 0 {
		parts = append(parts, "○"+strconv.Itoa(n.nStopped))
	}
	return strings.Join(parts, " ")
}

// aggColored は集約マークを状態色付きで組む（非選択行用）。色は状態マークの既存定数を
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

// joinPanes は左右のペイン行を "│" で縦に結合し、行数を h に揃える。左カラムは幅 leftW
// に固定（padDisplay は ANSI 対応のため色付き行でも幅が崩れない）。
func joinPanes(left, right []string, leftW, h int) []string {
	out := make([]string, h)
	for i := 0; i < h; i++ {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		out[i] = padDisplay(l, leftW) + "│" + r
	}
	return out
}

// noteLine はフッターの一時メッセージ行。
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
