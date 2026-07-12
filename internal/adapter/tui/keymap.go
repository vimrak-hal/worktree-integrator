package tui

import (
	"github.com/charmbracelet/bubbles/help"
	kb "github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
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
	// --- 共通（プロンプトに依らず効く） ---
	Quit   kb.Binding
	Doctor kb.Binding

	// --- ツリー ---
	Up           kb.Binding // 表示は "j/k 選択"（Down と対で 1 項目に見せる）
	Down         kb.Binding
	Collapse     kb.Binding // worktree 見出しの折りたたみトグル（space）
	CollapseNode kb.Binding // 折りたたむ（h / ←）
	ExpandNode   kb.Binding // 展開する（l / →）
	NextWorktree kb.Binding // 次の worktree 見出しへ（J）
	PrevWorktree kb.Binding // 前の worktree 見出しへ（K）
	SwitchTo     kb.Binding
	Restart      kb.Binding
	Stop         kb.Binding
	New          kb.Binding
	Delete       kb.Binding
	Alias        kb.Binding
	Refresh      kb.Binding

	// --- ログ（フォーカス廃止によりグローバル。ダイアログ非表示中はどこでも効く） ---
	Follow      kb.Binding
	Filter      kb.Binding
	Prev        kb.Binding
	Wrap        kb.Binding
	Top         kb.Binding // 表示は "g/G"（Bottom と対）
	Bottom      kb.Binding
	HalfDown    kb.Binding
	HalfUp      kb.Binding
	LineDown    kb.Binding // ダイアログ（doctor）内スクロール専用。通常ログでは廃止（j/k はツリー）
	LineUp      kb.Binding
	ClearFilter kb.Binding

	// --- フィルタ・doctor 結果（表示にも matching にも使う） ---
	// 作成・別名・削除の各モーダルは huh フォームへ移したため、切替（space）・全選択
	// （a）・削除確認（y）といったモーダル専用キーはここには持たない（huh が担う）。
	Confirm kb.Binding
	Cancel  kb.Binding
	Fix     kb.Binding
}

// newKeyMap は全バインドを構築する。
func newKeyMap() keyMap {
	return keyMap{
		Quit:   kb.NewBinding(kb.WithKeys("q"), kb.WithHelp("q", "終了")),
		Doctor: kb.NewBinding(kb.WithKeys("!"), kb.WithHelp("!", "doctor")),

		Up:   kb.NewBinding(kb.WithKeys("k", "up"), kb.WithHelp("j/k", "選択")),
		Down: kb.NewBinding(kb.WithKeys("j", "down"), kb.WithHelp("j/k", "選択")),
		// Collapse の実効キーは半角スペース。KeyMsg.String() は KeySpace / スペース rune の
		// どちらでも " " になるため、" " で一意に照合できる。
		Collapse: kb.NewBinding(kb.WithKeys(" "), kb.WithHelp("Space", "折りたたみ")),
		// 旧フォーカス切替キー（h/l/←/→）は空いたのでツリーの折りたたみ/展開へ割り当てる。
		CollapseNode: kb.NewBinding(kb.WithKeys("h", "left"), kb.WithHelp("h/l", "折/展")),
		ExpandNode:   kb.NewBinding(kb.WithKeys("l", "right"), kb.WithHelp("h/l", "折/展")),
		NextWorktree: kb.NewBinding(kb.WithKeys("J"), kb.WithHelp("J/K", "wt 移動")),
		PrevWorktree: kb.NewBinding(kb.WithKeys("K"), kb.WithHelp("J/K", "wt 移動")),
		SwitchTo:     kb.NewBinding(kb.WithKeys("enter", "s"), kb.WithHelp("Enter/s", "switch")),
		Restart:      kb.NewBinding(kb.WithKeys("r"), kb.WithHelp("r", "再起動")),
		Stop:         kb.NewBinding(kb.WithKeys("x"), kb.WithHelp("x", "stop")),
		New:          kb.NewBinding(kb.WithKeys("n"), kb.WithHelp("n", "作成")),
		Delete:       kb.NewBinding(kb.WithKeys("D"), kb.WithHelp("D", "削除")),
		Alias:        kb.NewBinding(kb.WithKeys("a"), kb.WithHelp("a", "別名")),
		Refresh:      kb.NewBinding(kb.WithKeys("R"), kb.WithHelp("R", "更新")),

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
	}
}

// newHelp はヘルプ行の描画器を作る。charm 系ツール風に、キー部分を colorAccent の太字、
// 説明を faint、項目区切りを faint の " · " にする（背景色は使わずテーマ追従を保つ）。
func newHelp() help.Model {
	h := help.New()
	faint := lipgloss.NewStyle().Faint(true)
	h.Styles.ShortKey = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	h.Styles.ShortDesc = faint
	h.Styles.ShortSeparator = faint
	h.ShortSeparator = " · "
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

// contextBindings は現在の文脈（フォーム / フィルタ入力 / doctor ダイアログ / 通常）で
// 表示すべきキーヘルプの並びを返す。フォーカスは廃止したため、通常時はツリーのキーと
// グローバル化したログのキーを 1 行にまとめる（幅超過分は help.Model が … で省略する）。
func (m *model) contextBindings() []kb.Binding {
	// huh フォーム表示中は huh の操作を日本語で要約する（キーの実処理は huh 側。ここは
	// 表示専用）。フォーム状態は formController に問い合わせ、開いていればその並びを使う。
	if b, ok := m.forms.contextBindings(); ok {
		return b
	}
	if m.log.prompt == promptFilter {
		return []kb.Binding{
			staticHelp("入力でフィルタ", ""),
			withHelp(m.keys.Confirm, "Enter", "確定"),
			withHelp(m.keys.ClearFilter, "Esc", "解除"),
		}
	}
	if m.doctor.doctorMode {
		return []kb.Binding{
			m.keys.Fix,
			withHelp(m.keys.LineUp, "j/k", "スクロール"),
			withHelp(m.keys.HalfUp, "d/u", "半ページ"),
			withHelp(m.keys.Top, "g/G", "先頭/末尾"),
			withHelp(m.keys.Cancel, "Esc", "閉じる"),
		}
	}
	// 通常時: ツリーのキー + グローバル化したログのキーを 1 行に。1 行に収まる主要キーへ
	// 絞る（全キーは docs/tui.md の表を参照。幅超過分は help.Model が末尾から … で省略する）。
	bindings := []kb.Binding{
		m.keys.Up, // "j/k 選択"
		withHelp(m.keys.CollapseNode, "h/l", "開閉"), // 折りたたみ/展開
		m.keys.SwitchTo, // "Enter/s switch"
		m.keys.New,
		m.keys.Delete,
		m.keys.Doctor,
		m.keys.Follow,
		m.keys.Filter,
		m.keys.Prev,
	}
	if m.log.filter != "" {
		// フィルタ適用中のみ解除キーを見せる（Esc でクリア）。未適用時に出すと Esc の
		// 対象が無く誤解を招くため条件付き。
		bindings = append(bindings, withHelp(m.keys.ClearFilter, "Esc", "フィルタ解除"))
	}
	bindings = append(bindings, m.keys.Quit)
	return bindings
}

// updateKey はキー入力をさばく。ダイアログ（フォーム / フィルタ入力 / doctor 結果）を
// 最優先で処理し、その後にグローバルキー・通常キー（ツリー + グローバル化したログ）へ
// 落とす。フォーカスの概念は無い（ツリーのキーとログのキーは衝突しないため両方を通常
// キーとして一括で受ける）。Update から KeyMsg のルーティング先として呼ばれる。
func (m *model) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}

	// huh フォーム表示中は Ctrl-C 以外の全キーをフォームが消費する。ただし Esc は
	// この層で横取りする: MultiSelect のフィルタ入力中だけは huh に渡し（フィルタ
	// 確定/解除）、それ以外は TUI がフォーム中止として畳む。huh の KeyMap.Quit に esc を
	// 足す方式だと、フィルタ入力中の Esc までフォーム全体の中止に化けて入力済みの内容が
	// 消えるため、フォーカス中フィールドの状態をここで判定する。
	if m.forms.form != nil {
		if msg.Type == tea.KeyEsc && !formFiltering(m.forms.form) {
			m.forms.clear()
			return m, nil
		}
		return m.updateForm(msg)
	}

	if m.log.prompt == promptFilter {
		return m, m.log.updateFilterKey(msg)
	}

	if m.doctor.doctorMode {
		return m.updateDoctorKey(msg)
	}

	switch {
	case kb.Matches(msg, m.keys.Quit):
		if m.op.opRunning {
			// 操作の途中で抜けると中断されるため、完了を待つ（強制終了は Ctrl-C）。
			m.op.quitAfterOp = true
			m.op.note, m.op.noteErr = "実行中の操作の完了を待って終了します…（強制終了は Ctrl-C）", false
			return m, nil
		}
		return m, tea.Quit
	case kb.Matches(msg, m.keys.Doctor):
		return m.startOp("doctor", m.doctorCmd(false))
	}

	return m.updateNormalKey(msg)
}

// updateDoctorKey は doctor 結果ダイアログ中のキー操作。モーダルなので j/k・d/u・g/G を
// すべてダイアログ内スクロールに使える（ツリー・ログとは衝突しない）。q / Esc は終了では
// なくダイアログを閉じる。スクロールは専用の dvp に効き、背後のログ位置は動かさない。
func (m *model) updateDoctorKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case kb.Matches(msg, m.keys.Cancel), kb.Matches(msg, m.keys.Quit):
		m.doctor.close()
	case kb.Matches(msg, m.keys.Fix):
		return m.startOp("doctor", m.doctorCmd(true))
	case kb.Matches(msg, m.keys.LineDown):
		m.doctor.dvp.ScrollDown(1)
	case kb.Matches(msg, m.keys.LineUp):
		m.doctor.dvp.ScrollUp(1)
	case kb.Matches(msg, m.keys.HalfDown):
		m.doctor.dvp.HalfPageDown()
	case kb.Matches(msg, m.keys.HalfUp):
		m.doctor.dvp.HalfPageUp()
	case kb.Matches(msg, m.keys.Top):
		m.doctor.dvp.GotoTop()
	case kb.Matches(msg, m.keys.Bottom):
		m.doctor.dvp.GotoBottom()
	}
	return m, nil
}

// updateNormalKey はダイアログ非表示中の通常キー。ツリーのキーと、フォーカス廃止により
// グローバル化したログのキーを 1 か所で受ける（両者は衝突しないため）。ログのスクロールは
// j/k 1 行を廃止し、ホイールと d/u（半ページ）・g/G（先頭/末尾）で代替する。
func (m *model) updateNormalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	// --- ツリー ---
	case kb.Matches(msg, m.keys.Down):
		return m, m.moveSel(1)
	case kb.Matches(msg, m.keys.Up):
		return m, m.moveSel(-1)
	case kb.Matches(msg, m.keys.NextWorktree):
		m.tree.jumpWorktree(1)
		return m, nil
	case kb.Matches(msg, m.keys.PrevWorktree):
		m.tree.jumpWorktree(-1)
		return m, nil
	case kb.Matches(msg, m.keys.Collapse):
		m.tree.toggleCollapse(m.cfg)
		return m, nil
	case kb.Matches(msg, m.keys.CollapseNode):
		m.tree.setCollapsed(m.cfg, true)
		return m, nil
	case kb.Matches(msg, m.keys.ExpandNode):
		m.tree.setCollapsed(m.cfg, false)
		return m, nil
	case kb.Matches(msg, m.keys.SwitchTo):
		if wt, ok := m.tree.selectedWorktree(); ok {
			return m.startOp("switch "+wt, m.switchCmd(wt, false))
		}
	case kb.Matches(msg, m.keys.Restart):
		if wt, ok := m.tree.selectedWorktree(); ok {
			return m.startOp("switch --restart "+wt, m.switchCmd(wt, true))
		}
	case kb.Matches(msg, m.keys.Stop):
		if wt, ok := m.tree.selectedWorktree(); ok {
			return m.startOp("stop "+wt, m.stopCmd(wt))
		}
	case kb.Matches(msg, m.keys.Refresh):
		return m, m.treesCmd()
	case kb.Matches(msg, m.keys.New):
		if m.op.opRunning {
			// フォームを入力し終えてから弾かれる無駄を防ぐため、候補取得の発行前に
			// opRunning を弾く。R（treesCmd）は読み取り専用なのでガード不要。
			m.op.note, m.op.noteErr = "別の操作を実行中です", true
			return m, nil
		}
		// reposMsg 受信で作成フォームを開く（名前とリポジトリ選択は 1 枚のフォームに
		// 統合されている）。
		return m, m.reposCmd()
	case kb.Matches(msg, m.keys.Delete):
		if wt, ok := m.tree.selectedWorktree(); ok {
			return m, m.forms.openRemove(wt, m.dialogInnerW(), m.dialogFormH())
		}
	case kb.Matches(msg, m.keys.Alias):
		if wt, ok := m.tree.selectedWorktree(); ok {
			return m, m.forms.openAlias(wt, m.tree.selectedAlias(), m.dialogInnerW(), m.dialogFormH())
		}

	// --- ログ（グローバル） ---
	case kb.Matches(msg, m.keys.Follow):
		m.log.toggleFollow()
	case kb.Matches(msg, m.keys.Filter):
		m.log.beginFilter()
	case kb.Matches(msg, m.keys.Prev):
		m.log.prev = !m.log.prev
		// パスが変わる（.prev ⇔ 現行）ため即座に再解決する。tailer は
		// applyResolved のパス変化検出でリセットされる。
		return m, m.resolveCmd()
	case kb.Matches(msg, m.keys.Wrap):
		m.log.wrap = !m.log.wrap
		m.log.rebuild()
	case kb.Matches(msg, m.keys.ClearFilter):
		m.log.clearFilter()
	case kb.Matches(msg, m.keys.Top):
		m.log.follow = false
		m.log.vp.GotoTop()
	case kb.Matches(msg, m.keys.Bottom):
		m.log.vp.GotoBottom()
	case kb.Matches(msg, m.keys.HalfDown):
		m.log.follow = false
		m.log.vp.HalfPageDown()
	case kb.Matches(msg, m.keys.HalfUp):
		m.log.follow = false
		m.log.vp.HalfPageUp()
	}
	return m, nil
}
