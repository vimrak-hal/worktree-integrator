package tui

import (
	"context"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/vimrak-hal/worktree-integrator/internal/app/server"
	"github.com/vimrak-hal/worktree-integrator/internal/app/tree"
	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/statedir"
)

// viewID は表示中のビュー。1 画面 3 ビュー（ログ / ステータス / worktree）を
// キーで切り替える。
type viewID int

const (
	viewLogs viewID = iota
	viewStatus
	viewTrees
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
	fw  *forwarder

	width, height int
	ready         bool
	view          viewID

	// --- ログビュー ---
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

	// --- ステータスビュー ---
	status    *server.StatusResult
	statusSel int

	// --- worktree ビュー ---
	trees    *tree.ListResult
	treesErr string
	treeSel  int
	// opRunning 中は新しい switch / stop を受け付けない（1 つずつ実行する）。
	opRunning bool
	opLabel   string
	// quitAfterOp は操作中に終了要求があったことを表す（完了を待って終了する）。
	quitAfterOp bool
	// events は switch / stop のライフサイクルイベントの履歴（新しい順に表示）。
	events []string

	// note はフッターの一時メッセージ（直近の操作結果・警告）。
	note    string
	noteErr bool
}

func newModel(ctx context.Context, cfg *config.File, root statedir.Root, fw *forwarder) *model {
	input := textinput.New()
	input.Prompt = "/"
	input.Placeholder = "フィルタ（部分一致）"
	return &model{
		ctx:    ctx,
		root:   root,
		cfg:    cfg,
		fw:     fw,
		view:   viewLogs,
		follow: true,
		wrap:   true,
		input:  input,
		tails:  map[string]*tailer{},
		bufs:   map[string]*ring{},
		merged: newRing(mergedRingCap),
	}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.resolveCmd(), m.treesCmd(), tailTick(), resolveTick(), treesTick())
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// ヘッダー 2 行（タブ・コンテキスト）とフッター 2 行（メッセージ・キー）を
		// 除いた残りが本文。
		m.vp = viewport.New(msg.Width, max(1, msg.Height-4))
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

	case treesTickMsg:
		// worktree 一覧はファイルスキャンを伴い重いので、見ているときだけ自動更新する
		//（それ以外は R・操作完了時に更新される）。
		if m.view == viewTrees && !m.opRunning {
			return m, tea.Batch(m.treesCmd(), treesTick())
		}
		return m, treesTick()

	case resolvedMsg:
		m.applyResolved(msg)
		return m, nil

	case treesMsg:
		if msg.err != nil {
			m.treesErr = msg.err.Error()
		} else {
			m.treesErr = ""
			m.trees = msg.res
			if n := len(msg.res.Worktrees); m.treeSel >= n {
				m.treeSel = max(0, n-1)
			}
		}
		return m, nil

	case serverEventMsg:
		m.events = append(m.events, msg.line)
		if len(m.events) > 100 {
			m.events = m.events[len(m.events)-100:]
		}
		return m, nil

	case opDoneMsg:
		m.opRunning = false
		m.opLabel = ""
		m.note, m.noteErr = msg.summary, msg.err != nil
		if msg.err != nil {
			m.note = msg.summary + "（" + msg.err.Error() + "）"
		}
		if m.quitAfterOp {
			return m, tea.Quit
		}
		// 切り替え・停止でログパスと状態が変わったはずなので、即座に再解決する。
		return m, tea.Batch(m.resolveCmd(), m.treesCmd())

	case tea.MouseMsg:
		if m.view == viewLogs {
			if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonWheelUp {
				m.follow = false
			}
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			return m, cmd
		}
		return m, nil

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
		if m.opRunning {
			// 切り替え・停止の途中で抜けると操作が中断されるため、完了を待つ
			//（強制終了は Ctrl-C）。
			m.quitAfterOp = true
			m.note, m.noteErr = "実行中の操作の完了を待って終了します…（強制終了は Ctrl-C）", false
			return m, nil
		}
		return m, tea.Quit
	case "1":
		m.view = viewLogs
		return m, nil
	case "2":
		m.view = viewStatus
		return m, nil
	case "3":
		m.view = viewTrees
		return m, m.treesCmd()
	case "tab":
		m.view = (m.view + 1) % 3
		if m.view == viewTrees {
			return m, m.treesCmd()
		}
		return m, nil
	}

	switch m.view {
	case viewLogs:
		return m.updateLogsKey(msg)
	case viewStatus:
		return m.updateStatusKey(msg)
	case viewTrees:
		return m.updateTreesKey(msg)
	}
	return m, nil
}

// updateLogsKey はログビューのキー操作。
func (m *model) updateLogsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
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

// updateStatusKey はステータスビューのキー操作。Enter で選択行のログへ跳ぶ。
func (m *model) updateStatusKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	rows := 0
	if m.status != nil {
		rows = len(m.status.Rows)
	}
	switch msg.String() {
	case "j", "down":
		if m.statusSel < rows-1 {
			m.statusSel++
		}
	case "k", "up":
		if m.statusSel > 0 {
			m.statusSel--
		}
	case "enter", "l":
		if m.status != nil && m.statusSel < rows {
			row := m.status.Rows[m.statusSel]
			m.selKey = row.Repo + "/" + row.Server
			m.view = viewLogs
			m.follow = true
			m.rebuildLog()
		}
	}
	return m, nil
}

// updateTreesKey は worktree ビューのキー操作。Enter / s で選択 worktree への
// switch、r で --restart 付き switch、x でその worktree のサーバー停止、l で
// その worktree に絞ったログ表示へ跳ぶ。
func (m *model) updateTreesKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	rows := 0
	if m.trees != nil {
		rows = len(m.trees.Worktrees)
	}
	selected := func() (string, bool) {
		if m.trees == nil || m.treeSel >= rows {
			return "", false
		}
		return m.trees.Worktrees[m.treeSel].Name, true
	}
	startOp := func(label string, cmd tea.Cmd) (tea.Model, tea.Cmd) {
		if m.opRunning {
			m.note, m.noteErr = "別の操作を実行中です", true
			return m, nil
		}
		m.opRunning = true
		m.opLabel = label
		m.note, m.noteErr = "", false
		return m, cmd
	}

	switch msg.String() {
	case "j", "down":
		if m.treeSel < rows-1 {
			m.treeSel++
		}
	case "k", "up":
		if m.treeSel > 0 {
			m.treeSel--
		}
	case "R":
		return m, m.treesCmd()
	case "enter", "s":
		if name, ok := selected(); ok {
			return startOp("switch "+name, m.switchCmd(name, false))
		}
	case "r":
		if name, ok := selected(); ok {
			return startOp("switch --restart "+name, m.switchCmd(name, true))
		}
	case "x":
		if name, ok := selected(); ok {
			return startOp("stop "+name, m.stopCmd(name))
		}
	case "l":
		if name, ok := selected(); ok {
			m.scope = name
			m.selKey = ""
			m.view = viewLogs
			m.follow = true
			return m, m.resolveCmd()
		}
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

// applyResolved は再解決の結果（設定・ログ対象・ステータス）をモデルへ写す。
// パスが変わった対象（外部 — MCP のエージェントや別の CLI — による switch を含む）は
// tailer とバッファを作り直し、新しいログを先頭（末尾 initialWindow バイト）から
// 読み直す。マージバッファは意図的に残す: 切り替えをまたいだ時系列が 1 本の流れとして
// 見える。
func (m *model) applyResolved(msg resolvedMsg) {
	if msg.err != nil {
		m.note, m.noteErr = "再解決に失敗: "+msg.err.Error(), true
		return
	}
	if msg.warn != "" {
		m.note, m.noteErr = msg.warn, true
	}
	m.cfg = msg.cfg
	m.status = msg.status
	if m.status != nil && m.statusSel >= len(m.status.Rows) {
		m.statusSel = max(0, len(m.status.Rows)-1)
	}

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
