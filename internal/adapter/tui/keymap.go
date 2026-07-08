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
//
// 不変条件: 各 Binding の WithKeys は移行前の msg.String() スイッチと完全に一致
// させる（例: ツリーの Up=k/up・Down=j/down、ログの HalfDown=d/pgdown）。表示用の
// キー文字列（WithHelp の第 1 引数）は現行 helpLine の文言を再現するための見た目
// であり、実効キーには影響しない。
type keyMap struct {
	// --- 共通（フォーカスやプロンプトに依らず効く） ---
	Quit   kb.Binding
	Focus  kb.Binding // ペイン切替（実キー: tab/left/right/h/l）
	Doctor kb.Binding

	// --- ツリー（左ペイン） ---
	Up       kb.Binding // 表示は "j/k 選択"（Down と対で 1 項目に見せる）
	Down     kb.Binding
	SwitchTo kb.Binding
	Restart  kb.Binding
	Stop     kb.Binding
	New      kb.Binding
	Delete   kb.Binding
	Alias    kb.Binding
	Refresh  kb.Binding

	// --- ログ（右ペイン） ---
	Follow      kb.Binding
	Filter      kb.Binding
	Prev        kb.Binding
	Wrap        kb.Binding
	Top         kb.Binding // 表示は "g/G"（Bottom と対）
	Bottom      kb.Binding
	HalfDown    kb.Binding
	HalfUp      kb.Binding
	LineDown    kb.Binding
	LineUp      kb.Binding // 表示は "j/k スクロール"
	ClearFilter kb.Binding

	// --- フィルタ・doctor 結果（表示にも matching にも使う） ---
	// 作成・別名・削除の各モーダルは huh フォームへ移したため、切替（space）・全選択
	// （a）・削除確認（y）といったモーダル専用キーはここには持たない（huh が担う）。
	Confirm kb.Binding
	Cancel  kb.Binding
	Fix     kb.Binding

	// --- 表示専用（フォーカス切替の文脈別ラベル） ---
	// 現行 helpLine の「Tab→ログ」「Tab→ツリー」という文脈依存ラベルを再現する。
	// matching は Focus が担うため、これらは help への露出のためだけに存在する
	// （help はキーの無い Binding を描画しないので実キーを 1 つ持たせている）。
	FocusToLog  kb.Binding
	FocusToTree kb.Binding
}

// newKeyMap は全バインドを構築する。
func newKeyMap() keyMap {
	return keyMap{
		Quit:   kb.NewBinding(kb.WithKeys("q"), kb.WithHelp("q", "終了")),
		Focus:  kb.NewBinding(kb.WithKeys("tab", "left", "right", "h", "l"), kb.WithHelp("Tab", "ペイン切替")),
		Doctor: kb.NewBinding(kb.WithKeys("!"), kb.WithHelp("!", "doctor")),

		Up:       kb.NewBinding(kb.WithKeys("k", "up"), kb.WithHelp("j/k", "選択")),
		Down:     kb.NewBinding(kb.WithKeys("j", "down"), kb.WithHelp("j/k", "選択")),
		SwitchTo: kb.NewBinding(kb.WithKeys("enter", "s"), kb.WithHelp("Enter/s", "switch")),
		Restart:  kb.NewBinding(kb.WithKeys("r"), kb.WithHelp("r", "再起動")),
		Stop:     kb.NewBinding(kb.WithKeys("x"), kb.WithHelp("x", "stop")),
		New:      kb.NewBinding(kb.WithKeys("n"), kb.WithHelp("n", "作成")),
		Delete:   kb.NewBinding(kb.WithKeys("D"), kb.WithHelp("D", "削除")),
		Alias:    kb.NewBinding(kb.WithKeys("a"), kb.WithHelp("a", "別名")),
		Refresh:  kb.NewBinding(kb.WithKeys("R"), kb.WithHelp("R", "更新")),

		Follow:      kb.NewBinding(kb.WithKeys("f"), kb.WithHelp("f", "追従")),
		Filter:      kb.NewBinding(kb.WithKeys("/"), kb.WithHelp("/", "フィルタ")),
		Prev:        kb.NewBinding(kb.WithKeys("p"), kb.WithHelp("p", "前世代")),
		Wrap:        kb.NewBinding(kb.WithKeys("w"), kb.WithHelp("w", "折り返し")),
		Top:         kb.NewBinding(kb.WithKeys("g"), kb.WithHelp("g/G", "")),
		Bottom:      kb.NewBinding(kb.WithKeys("G"), kb.WithHelp("G", "")),
		HalfDown:    kb.NewBinding(kb.WithKeys("d", "pgdown"), kb.WithHelp("d", "pgdown")),
		HalfUp:      kb.NewBinding(kb.WithKeys("u", "pgup"), kb.WithHelp("u", "pgup")),
		LineDown:    kb.NewBinding(kb.WithKeys("j", "down"), kb.WithHelp("j/k", "スクロール")),
		LineUp:      kb.NewBinding(kb.WithKeys("k", "up"), kb.WithHelp("j/k", "スクロール")),
		ClearFilter: kb.NewBinding(kb.WithKeys("esc"), kb.WithHelp("esc", "解除")),

		Confirm: kb.NewBinding(kb.WithKeys("enter"), kb.WithHelp("Enter", "確定/実行")),
		Cancel:  kb.NewBinding(kb.WithKeys("esc"), kb.WithHelp("Esc", "中止/閉じる")),
		Fix:     kb.NewBinding(kb.WithKeys("F"), kb.WithHelp("F", "--fix 実行")),

		FocusToLog:  kb.NewBinding(kb.WithKeys("tab"), kb.WithHelp("Tab", "→ログ")),
		FocusToTree: kb.NewBinding(kb.WithKeys("tab"), kb.WithHelp("Tab", "→ツリー")),
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

// withHelp は既存 Binding の表示（キー/ラベル）だけを差し替えた複製を返す。同じキーへ
// 文脈ごとに異なる説明を出す（例: Enter を「確定」「次へ」「実行」と出し分ける）ための
// 表示専用ヘルパで、実効キー（WithKeys）は変えない。
func withHelp(b kb.Binding, keyLabel, desc string) kb.Binding {
	b.SetHelp(keyLabel, desc)
	return b
}

// staticHelp はキーに紐づかない説明文（プロンプト冒頭の操作説明・「他キー 中止」など）を
// ショートヘルプに 1 項目として載せるための表示専用 Binding。help はキーの無い Binding を
// 描画しないため、Matches に決して渡さないダミーキーを与えて Enabled にしている。
func staticHelp(keyLabel, desc string) kb.Binding {
	return kb.NewBinding(kb.WithKeys("\x00"), kb.WithHelp(keyLabel, desc))
}

// contextBindings は現在の文脈（プロンプト各種 / doctor 結果 / フォーカス）で表示すべき
// キーヘルプの並びを返す。項目・順序・日本語ラベルは移行前の helpLine を踏襲する。
func (m *model) contextBindings() []kb.Binding {
	// huh フォーム表示中は huh の操作を日本語で要約する（キーの実処理は huh 側。
	// ここは表示専用なので固定の Binding 列で統一感を優先する）。
	if m.form != nil {
		return []kb.Binding{
			staticHelp("Tab/↓", "次へ"),
			staticHelp("space", "切替"),
			staticHelp("Enter", "確定"),
			staticHelp("Esc", "中止"),
		}
	}
	if m.prompt == promptFilter {
		return []kb.Binding{
			staticHelp("入力でフィルタ", ""),
			withHelp(m.keys.Confirm, "Enter", "確定"),
			withHelp(m.keys.ClearFilter, "Esc", "解除"),
		}
	}
	if m.doctorMode {
		return []kb.Binding{
			m.keys.Fix,
			withHelp(m.keys.LineUp, "j/k", "スクロール"),
			withHelp(m.keys.Cancel, "Esc", "閉じる"),
		}
	}
	if m.focus == focusTree {
		return []kb.Binding{
			m.keys.Up, // "j/k 選択"
			m.keys.SwitchTo,
			m.keys.Restart,
			m.keys.Stop,
			m.keys.New,
			m.keys.Delete,
			m.keys.Alias,
			m.keys.Doctor,
			m.keys.Refresh,
			m.keys.FocusToLog,
			m.keys.Quit,
		}
	}
	return []kb.Binding{
		m.keys.LineUp, // "j/k スクロール"
		m.keys.Follow,
		m.keys.Filter,
		m.keys.Prev,
		m.keys.Wrap,
		m.keys.Top, // "g/G"
		m.keys.FocusToTree,
		m.keys.Quit,
	}
}
