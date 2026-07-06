package tui

import (
	"context"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/statedir"
)

// バッファの保持行数。単一サーバーのバッファと、全サーバーを到着順に混ぜた
// マージバッファ。
const (
	targetRingCap = 4000
	mergedRingCap = 8000
	// maxRender はビューポートへ流す最大行数。フィルタ後にこれを超える分は古い側を
	// 落とす（描画コストの上限）。
	maxRender = 2000
)

// logTarget は 1 つの表示対象ログ（repo × server）。パスはワークフロー
// （App.ServerLogs）が解決した値で、定期的な再解決で更新される。
type logTarget struct {
	repo, server, path string
	// missing はログファイルがまだ存在しないこと（worktree 名指定のスコープのみ）。
	missing bool
	// readErr は直近のポーリングでの読み取りエラー（表示用）。
	readErr string
}

// key は対象の同一性（バッファ・tailer のキーであり、表示タグでもある）。repo と
// server の名前は '/' を含まないよう検証済みのため、この連結は単射である。
func (t logTarget) key() string { return t.repo + "/" + t.server }

// model は TUI 全体の状態。Bubble Tea の Elm アーキテクチャに乗り、更新は
// Update・描画は View に閉じる。
type model struct {
	ctx  context.Context
	root statedir.Root
	// cfg は直近に正常に読み込めた設定。MCP サーバーと同様に定期的に再読み込みし、
	// 編集は TUI の再起動なしで反映される（読めない間は直近の正常値で動き続ける）。
	cfg *config.File

	width, height int
	ready         bool

	// scope は worktree 名での絞り込み（空 = 稼働中サーバーのログすべて）。
	scope string
	// prev は 1 世代前のログ（.prev）を見るモード。
	prev bool
	// follow は末尾追従（tail -f 相当）。上方向へのスクロールで解除される。
	follow bool
	// wrap は長い行の折り返し（オフなら端末幅で切り詰め）。
	wrap bool
	// filter は部分一致（大文字小文字を無視）の表示フィルタ。
	filter    string
	filtering bool
	input     textinput.Model
	targets   []logTarget
	tails     map[string]*tailer
	bufs      map[string]*ring
	merged    *ring
	// selKey は選択中の対象（logTarget.key()）。空はマージ表示（すべて）。
	selKey string
	vp     viewport.Model

	// note はフッターの一時メッセージ（直近の警告など）。
	note    string
	noteErr bool
}

func newModel(ctx context.Context, cfg *config.File, root statedir.Root) *model {
	input := textinput.New()
	input.Prompt = "/"
	input.Placeholder = "フィルタ（部分一致）"
	return &model{
		ctx:    ctx,
		root:   root,
		cfg:    cfg,
		follow: true,
		wrap:   true,
		input:  input,
		tails:  map[string]*tailer{},
		bufs:   map[string]*ring{},
		merged: newRing(mergedRingCap),
	}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.resolveCmd(), tailTick(), resolveTick())
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// ヘッダー 1 行（対象バー）とフッター 2 行（メッセージ・キー）を除いた残りが
		// 本文。
		m.vp = viewport.New(msg.Width, max(1, msg.Height-3))
		m.ready = true
		m.rebuildLog()
		return m, nil

	case tailTickMsg:
		if m.pollTails() {
			m.rebuildLog()
		}
		return m, tailTick()

	case resolveTickMsg:
		return m, tea.Batch(m.resolveCmd(), resolveTick())

	case resolvedMsg:
		m.applyResolved(msg)
		return m, nil

	case tea.MouseMsg:
		if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonWheelUp {
			m.follow = false
		}
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		return m.updateKey(msg)
	}
	return m, nil
}

// updateKey はキー入力をさばく。フィルタ入力中はテキスト入力を優先する。
func (m *model) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}

	if m.filtering {
		switch msg.Type {
		case tea.KeyEnter:
			m.filtering = false
			m.input.Blur()
		case tea.KeyEscape:
			m.filtering = false
			m.filter = ""
			m.input.SetValue("")
			m.input.Blur()
			m.rebuildLog()
		default:
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			// ライブフィルタ: 1 打鍵ごとに絞り込みが反映される。
			m.filter = m.input.Value()
			m.rebuildLog()
			return m, cmd
		}
		return m, nil
	}

	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "left", "h":
		m.cycleTarget(-1)
	case "right", "l":
		m.cycleTarget(+1)
	case "f":
		m.follow = !m.follow
		if m.follow {
			m.vp.GotoBottom()
		}
	case "/":
		m.filtering = true
		m.input.Focus()
	case "esc":
		switch {
		case m.filter != "":
			m.filter = ""
			m.input.SetValue("")
			m.rebuildLog()
		case m.scope != "":
			m.scope = ""
			m.note, m.noteErr = "", false
			return m, m.resolveCmd()
		}
	case "p":
		m.prev = !m.prev
		// パスが変わる（.prev ⇔ 現行）ため即座に再解決する。tailer は
		// applyResolved のパス変化検出でリセットされる。
		return m, m.resolveCmd()
	case "w":
		m.wrap = !m.wrap
		m.rebuildLog()
	case "g":
		m.follow = false
		m.vp.GotoTop()
	case "G":
		m.vp.GotoBottom()
	case "j", "down":
		m.follow = false
		m.vp.ScrollDown(1)
	case "k", "up":
		m.follow = false
		m.vp.ScrollUp(1)
	case "d", "pgdown":
		m.follow = false
		m.vp.HalfPageDown()
	case "u", "pgup":
		m.follow = false
		m.vp.HalfPageUp()
	}
	return m, nil
}

// cycleTarget は選択対象を「すべて → 各対象 → すべて…」の順で巡回させる。
func (m *model) cycleTarget(delta int) {
	// 位置 0 = マージ表示（すべて）、1..n = targets。
	pos := 0
	for i, t := range m.targets {
		if t.key() == m.selKey {
			pos = i + 1
			break
		}
	}
	n := len(m.targets) + 1
	pos = ((pos+delta)%n + n) % n
	if pos == 0 {
		m.selKey = ""
	} else {
		m.selKey = m.targets[pos-1].key()
	}
	m.follow = true
	m.rebuildLog()
}

// applyResolved は再解決の結果（設定・ログ対象）をモデルへ写す。パスが変わった
// 対象（外部 — MCP のエージェントや別の CLI — による switch を含む）は tailer と
// バッファを作り直し、新しいログを先頭（末尾 initialWindow バイト）から読み直す。
// マージバッファは意図的に残す: 切り替えをまたいだ時系列が 1 本の流れとして見える。
func (m *model) applyResolved(msg resolvedMsg) {
	if msg.err != nil {
		m.note, m.noteErr = "再解決に失敗: "+msg.err.Error(), true
		return
	}
	if msg.warn != "" {
		m.note, m.noteErr = msg.warn, true
	}
	m.cfg = msg.cfg

	var targets []logTarget
	alive := map[string]bool{}
	for _, e := range msg.logs.Logs {
		t := logTarget{repo: e.Repo, server: e.Server, path: e.Path, missing: e.Missing}
		key := t.key()
		alive[key] = true
		if !t.missing {
			if old := m.tails[key]; old == nil || old.path != t.path {
				m.tails[key] = newTailer(t.path)
				m.bufs[key] = newRing(targetRingCap)
			}
		}
		targets = append(targets, t)
	}
	// 対象から消えたエントリ（設定から消えた・スコープ変更）は追跡をやめる。
	for key := range m.tails {
		if !alive[key] {
			delete(m.tails, key)
			delete(m.bufs, key)
		}
	}
	m.targets = targets
	if m.selKey != "" && !alive[m.selKey] {
		m.selKey = "" // 選択対象が消えたらマージ表示へ戻す
	}
	// 対象が入れ替わった直後に空画面を見せないため、増分を読んでから描き直す。
	m.pollTails()
	m.rebuildLog()
}

// pollTails は全対象の増分を読み、バッファへ追記する。追記があったかを返す。
func (m *model) pollTails() bool {
	changed := false
	for i := range m.targets {
		t := &m.targets[i]
		tail := m.tails[t.key()]
		if tail == nil {
			continue
		}
		lines, err := tail.poll()
		if err != nil {
			t.readErr = err.Error()
			continue
		}
		t.readErr = ""
		if len(lines) == 0 {
			continue
		}
		changed = true
		buf := m.bufs[t.key()]
		for _, text := range lines {
			buf.push(line{text: text})
			m.merged.push(line{tag: t.key(), text: text})
		}
	}
	return changed
}

// rebuildLog は現在の選択・フィルタからビューポートの内容を組み立て直す。
func (m *model) rebuildLog() {
	if !m.ready {
		return
	}
	var src []line
	if m.selKey == "" {
		src = m.merged.slice()
	} else if buf := m.bufs[m.selKey]; buf != nil {
		src = buf.slice()
	}

	filter := strings.ToLower(m.filter)
	rendered := make([]string, 0, len(src))
	for _, l := range src {
		if filter != "" && !strings.Contains(strings.ToLower(l.text), filter) &&
			!strings.Contains(strings.ToLower(l.tag), filter) {
			continue
		}
		text := colorizeLog(l.text)
		if l.tag != "" {
			text = tagStyle(l.tag).Render("["+l.tag+"]") + " " + text
		}
		rendered = append(rendered, text)
	}
	if len(rendered) > maxRender {
		rendered = rendered[len(rendered)-maxRender:]
	}
	if m.wrap {
		for i, text := range rendered {
			rendered[i] = wrapDisplay(text, m.vp.Width)
		}
	}
	m.vp.SetContent(strings.Join(rendered, "\n"))
	if m.follow {
		m.vp.GotoBottom()
	}
}
