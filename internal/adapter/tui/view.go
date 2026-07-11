package tui

import (
	"strconv"
	"strings"
)

// View は現在のモデルを 1 画面に描画する。レイアウトは lazygit 風の 2 ペイン: 固定 3 行の
// クローム（ペインタイトル行・note 行・ヘルプ行）と、その間の本文（左=ツリー、右=ログ）を
// "│" で縦に区切る。
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

// titleLine はペインタイトル行（左=WORKTREES、右=ログ）。フォーカス側が反転で強調される。
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

// logTitle は右ペインの見出し。ログペイン本体は後続で追加するため、この段では見出しの
// プレースホルダのみを描く。
func (m *model) logTitle() string {
	return m.paneTitle(" ログ ", focusLog)
}

// treeLines は左ペインの行を組む: スクロールするノード一覧の下に空行のフッターを置く。
func (m *model) treeLines(h int) []string {
	// 下部に置くフッター（空行）を先に組み、残りをノード領域にする。
	footer := []string{""}
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
		nodeLines = []string{"worktree がありません"}
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
		text := "▾ " + n.wt
		if n.alias != "" {
			text += " (" + n.alias + ")"
		}
		if n.broken {
			text += " (!)"
		}
		if i == m.sel {
			return stySelected.Render(text)
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

// rightLines は右ペインの行を返す。ログペイン本体は後続で追加するため、この段では
// プレースホルダの案内のみを表示する。
func (m *model) rightLines() []string {
	return []string{"サーバーノードを選択してください"}
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
