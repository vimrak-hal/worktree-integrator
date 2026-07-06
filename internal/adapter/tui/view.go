package tui

import (
	"strings"
)

// View は現在のモデルを 1 画面に描画する。レイアウトは固定 3 行のクローム
// （ヘッダー: 対象バー、フッター: メッセージ + キーヘルプ）と本文（ビューポート）。
func (m *model) View() string {
	if !m.ready {
		return "起動中…"
	}
	var b strings.Builder
	b.WriteString(truncLine(m.logsBar(), m.width))
	b.WriteString("\n")
	b.WriteString(m.vp.View())
	b.WriteString("\n")
	b.WriteString(truncLine(m.noteLine(), m.width))
	b.WriteString("\n")
	b.WriteString(truncLine(styHelp.Render(m.helpLine()), m.width))
	return b.String()
}

// logsBar はヘッダーの対象バー: 対象の巡回リストと、モードのフラグ（追従・前世代・
// worktree 絞り込み・フィルタ）。
func (m *model) logsBar() string {
	var parts []string
	sel := func(label string, active bool) string {
		if active {
			return stySelected.Render(" " + label + " ")
		}
		return " " + label + " "
	}
	parts = append(parts, sel("すべて", m.selKey == ""))
	for _, t := range m.targets {
		label := t.key()
		if t.missing {
			label += "(なし)"
		} else if t.readErr != "" {
			label += "(!)"
		}
		parts = append(parts, sel(label, m.selKey == t.key()))
	}

	var flags []string
	if m.follow {
		flags = append(flags, "追従")
	}
	if m.prev {
		flags = append(flags, "前世代(.prev)")
	}
	if m.scope != "" {
		flags = append(flags, "worktree:"+m.scope)
	}
	if m.filtering {
		flags = append(flags, m.input.View())
	} else if m.filter != "" {
		flags = append(flags, "/"+m.filter)
	}
	bar := strings.Join(parts, "|")
	if len(flags) > 0 {
		bar += "  " + styFlag.Render(strings.Join(flags, " "))
	}
	return bar
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

// helpLine はキーヘルプ。
func (m *model) helpLine() string {
	return "←/→ 対象切替  f 追従  / フィルタ  p 前世代  w 折り返し  j/k/g/G スクロール  Esc 解除  q 終了"
}

// truncLine は 1 行を表示幅 w に収める。
func truncLine(s string, w int) string {
	if w <= 0 {
		return s
	}
	return truncDisplay(s, w)
}
