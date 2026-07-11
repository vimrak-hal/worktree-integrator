package tui

import (
	"context"

	"github.com/charmbracelet/bubbles/help"
	kb "github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/statedir"
)

// focusID はキー入力を受けるペイン。左（ツリー）と右（ログ）の 2 ペインを表し、
// ペイン見出しはフォーカス側を反転で強調する。
type focusID int

const (
	focusTree focusID = iota
	focusLog
)

// model は TUI 全体の状態。Bubble Tea の Elm アーキテクチャに乗り、更新は
// Update・描画は View に閉じる。
type model struct {
	ctx  context.Context
	root statedir.Root
	// cfg は直近に正常に読み込めた設定。MCP サーバーと同様に定期的に再読み込みし、
	// 編集は TUI の再起動なしで反映される（読めない間は直近の正常値で動き続ける）。
	cfg *config.File

	// keys は全キーバインド、help はヘルプ行の描画器。キー処理は keys と
	// kb.Matches で照合し、ヘルプ行は contextBindings を help が描く。
	keys keyMap
	help help.Model

	width, height int
	ready         bool
	focus         focusID

	// note はフッターの一時メッセージ（直近の操作結果・警告）。
	note    string
	noteErr bool
}

func newModel(ctx context.Context, cfg *config.File, root statedir.Root) *model {
	return &model{
		ctx:   ctx,
		root:  root,
		cfg:   cfg,
		keys:  newKeyMap(),
		help:  newHelp(),
		focus: focusTree,
	}
}

func (m *model) Init() tea.Cmd { return nil }

// leftW は左ペイン（ツリー）の表示幅。端末幅の 1/3 を基準に、狭すぎ・広すぎを避けて
// 24〜40 桁に収める。
func (m *model) leftW() int {
	return clamp(m.width/3, 24, 40)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// ヘルプ行の幅超過時の省略（…）を端末幅に合わせる。
		m.help.Width = m.width
		m.ready = true
		return m, nil

	case tea.KeyMsg:
		return m.updateKey(msg)
	}
	return m, nil
}

// updateKey はキー入力をさばく。
func (m *model) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}

	switch {
	case kb.Matches(msg, m.keys.Quit):
		return m, tea.Quit
	}

	return m, nil
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
