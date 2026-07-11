package tui

import (
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

	// 左右ペインの中身は後続（A2: ツリー表示 / #7: ログペイン）で追加するため、この段では
	// プレースホルダ文言のみを描く。
	left := []string{"worktree がありません"}
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
