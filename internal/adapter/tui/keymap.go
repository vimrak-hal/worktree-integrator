package tui

import (
	"github.com/charmbracelet/bubbles/help"
	kb "github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"
)

// keyMap は TUI の全キーバインドを 1 か所に集約する。キー処理（Update）は
// kb.Matches でこの構造体の Binding と照合し、ヘルプ行（View）は各 Binding の
// 表示テキストから help.Model が組み立てる。「実効キー（WithKeys）」と「表示
// （WithHelp）」を 1 つの真実として持つことで、両者のズレを防ぐのが目的。
type keyMap struct {
	// --- 共通（フォーカスやプロンプトに依らず効く） ---
	Quit kb.Binding

	// --- ツリー（左ペイン） ---
	Up      kb.Binding // 表示は "j/k 選択"（Down と対で 1 項目に見せる）
	Down    kb.Binding
	Refresh kb.Binding
}

// newKeyMap は全バインドを構築する。
func newKeyMap() keyMap {
	return keyMap{
		Quit: kb.NewBinding(kb.WithKeys("q"), kb.WithHelp("q", "終了")),

		Up:      kb.NewBinding(kb.WithKeys("k", "up"), kb.WithHelp("j/k", "選択")),
		Down:    kb.NewBinding(kb.WithKeys("j", "down"), kb.WithHelp("j/k", "選択")),
		Refresh: kb.NewBinding(kb.WithKeys("R"), kb.WithHelp("R", "更新")),
	}
}

// newHelp はヘルプ行の描画器を作る。見た目を既存の styHelp（faint）に合わせ、区切りは
// 現行 helpLine の 2 スペースに寄せる（help のデフォルトは色付き " • " 区切り）。
func newHelp() help.Model {
	h := help.New()
	faint := lipgloss.NewStyle().Faint(true)
	h.Styles.ShortKey = faint
	h.Styles.ShortDesc = faint
	h.Styles.ShortSeparator = faint
	h.ShortSeparator = "  "
	return h
}

// contextBindings は現在の文脈で表示すべきキーヘルプの並びを返す。項目・順序・日本語
// ラベルは移行前の helpLine を踏襲する。
func (m *model) contextBindings() []kb.Binding {
	return []kb.Binding{
		m.keys.Up, // "j/k 選択"
		m.keys.Refresh,
		m.keys.Quit,
	}
}
