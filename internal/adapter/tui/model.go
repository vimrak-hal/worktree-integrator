package tui

import (
	"context"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/vimrak-hal/worktree-integrator/internal/app"
	"github.com/vimrak-hal/worktree-integrator/internal/app/server"
	"github.com/vimrak-hal/worktree-integrator/internal/app/tree"
	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
	coreserver "github.com/vimrak-hal/worktree-integrator/internal/core/server"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/statedir"
)

// focusID はキー入力を受けるペイン。lazygit 風に左（ツリー）と右（ログ）の 2 ペインを
// Tab で行き来する。
type focusID int

const (
	focusTree focusID = iota
	focusLog
)

// promptMode は最前面のモーダル入力の種類。promptNone 以外のときはキー入力を
// 各プロンプトのハンドラが最優先で消費する。
type promptMode int

const (
	promptNone promptMode = iota
	promptFilter
	promptCreateName
	promptCreateRepos
	promptAlias
	promptConfirmRemove
)

// バッファの保持行数と描画上限。単一サーバーのログだけを表示するため、マージ用の
// 大きなリングは持たない。
const (
	targetRingCap = 4000
	// maxRender はビューポートへ流す最大行数。フィルタ後にこれを超える分は古い側を
	// 落とす（描画コストの上限）。
	maxRender = 2000
)

// node はツリーの 1 行。worktree ノード（repo == ""）と、その配下のサーバーノード
// （repo != ""）の 2 種を 1 つの型で表す。
type node struct {
	// wt はこのノードが属する worktree 名（サーバーノードも親の名前を持つ）。
	wt    string
	alias string
	// broken は worktree ノードの壊れたチェックアウトの有無（表示の (!)）。
	broken bool

	// repo / server はサーバーノードの座標（worktree ノードでは空）。
	repo    string
	server  string
	running bool
	pid     int
	crashed bool
}

// isWorktree は worktree ノード（配下にサーバーノードを持つ見出し行）かどうか。
func (n node) isWorktree() bool { return n.repo == "" }

// key はノードの同一性。worktree ノードは wt 名、サーバーノードは
// wt + "\x00" + repo + "/" + server で単射に表す（repo / server は '/' を含まない
// よう検証済み、wt は '/' を含みうるが "\x00" 区切りで一意）。この key がログ対象・
// tailer・バッファのキー（curKey）でもある。
func (n node) key() string {
	if n.isWorktree() {
		return n.wt
	}
	return n.wt + "\x00" + n.repo + "/" + n.server
}

// splitKey はサーバーノードの key を (wt, repo, server) へ復元する。壊れた key
// （worktree ノードの key や不正な形）は ok=false。
func splitKey(key string) (wt, repo, server string, ok bool) {
	i := strings.IndexByte(key, 0)
	if i < 0 {
		return "", "", "", false
	}
	wt = key[:i]
	rs := key[i+1:]
	j := strings.IndexByte(rs, '/')
	if j < 0 {
		return "", "", "", false
	}
	return wt, rs[:j], rs[j+1:], true
}

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
	focus         focusID

	// --- ツリー（左ペイン） ---
	trees    *tree.ListResult
	treesErr string
	status   *server.StatusResult
	nodes    []node
	// sel はツリーのカーソル位置（nodes のインデックス）。
	sel int
	// treeTop はツリーの縦スクロール位置（可視域の先頭 nodes インデックス）。
	treeTop int

	// --- ログ（右ペイン） ---
	// curKey は表示中サーバーノードの key（空はプレースホルダ）。
	curKey     string
	curPath    string
	curMissing bool
	curReadErr string
	tails      map[string]*tailer
	bufs       map[string]*ring
	vp         viewport.Model
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

	// --- モーダル・結果ペイン ---
	prompt     promptMode
	createName string
	// promptTarget は alias / remove プロンプトの対象 worktree 名（プロンプトを
	// 開いた時点のカーソル位置を固定して保持する）。
	promptTarget string
	repos        *app.ReposResult
	repoChecked  []bool
	repoSel      int
	// doctorText / doctorMode は doctor 結果の右ペイン表示。doctorMode 中は vp の
	// 内容が doctorText になる（rebuildLog が出し分ける）。
	doctorText []string
	doctorMode bool

	// opRunning 中は新しい統合操作を受け付けない（1 つずつ実行する）。
	opRunning bool
	opLabel   string
	// quitAfterOp は操作中に終了要求があったことを表す（完了を待って終了する）。
	quitAfterOp bool
	// events はライフサイクル・作成進捗のイベント履歴（新しい順に表示）。
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
		focus:  focusTree,
		follow: true,
		wrap:   true,
		input:  input,
		tails:  map[string]*tailer{},
		bufs:   map[string]*ring{},
	}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.resolveCmd(), m.treesCmd(), tailTick(), resolveTick(), treesTick())
}

// leftW は左ペイン（ツリー）の表示幅。端末幅の 1/3 を基準に、狭すぎ・広すぎを避けて
// 24〜40 桁に収める。
func (m *model) leftW() int {
	return clamp(m.width/3, 24, 40)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		lw := m.leftW()
		// クローム 3 行（ペインタイトル行・note 行・ヘルプ行）を除いた残りが本文。
		m.vp = viewport.New(max(1, m.width-lw-1), max(1, m.height-3))
		m.ready = true
		m.rebuildLog()
		return m, nil

	case tailTickMsg:
		if m.pollTail() {
			m.rebuildLog()
		}
		return m, tailTick()

	case resolveTickMsg:
		return m, tea.Batch(m.resolveCmd(), resolveTick())

	case treesTickMsg:
		// 操作の実行中は worktree 一覧を触らない（完了時に取り直す）。
		if !m.opRunning {
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
		}
		m.buildNodes()
		if cmd := m.ensureSelection(); cmd != nil {
			return m, cmd
		}
		m.rebuildLog()
		return m, nil

	case reposMsg:
		if msg.err != nil {
			m.note, m.noteErr = "リポジトリ一覧の取得に失敗: "+msg.err.Error(), true
			m.prompt = promptNone
			return m, nil
		}
		m.repos = msg.res
		m.repoChecked = make([]bool, len(msg.res.Repos))
		for i := range m.repoChecked {
			m.repoChecked[i] = true
		}
		m.repoSel = 0
		m.prompt = promptCreateRepos
		return m, nil

	case eventMsg:
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
		if len(msg.doctorText) > 0 {
			m.doctorText = msg.doctorText
			m.doctorMode = true
			m.rebuildLog()
		}
		if m.quitAfterOp {
			return m, tea.Quit
		}
		// 切り替え・停止・作成・削除で状態・一覧・ログパスが変わったはずなので、
		// 即座に再解決する。
		return m, tea.Batch(m.resolveCmd(), m.treesCmd())

	case tea.MouseMsg:
		// フォーカスに関わらずホイールでログをスクロールする。上方向は追従を解除。
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

// updateKey はキー入力をさばく。モーダル（プロンプト）と結果ペイン（doctor）を
// 最優先で処理し、その後にグローバル・ペイン別のキーへ落とす。
func (m *model) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}

	switch m.prompt {
	case promptFilter:
		return m.updateFilterKey(msg)
	case promptCreateName:
		return m.updateCreateNameKey(msg)
	case promptAlias:
		return m.updateAliasKey(msg)
	case promptCreateRepos:
		return m.updateCreateReposKey(msg)
	case promptConfirmRemove:
		return m.updateConfirmRemoveKey(msg)
	}

	if m.doctorMode {
		return m.updateDoctorKey(msg)
	}

	switch msg.String() {
	case "q":
		if m.opRunning {
			// 操作の途中で抜けると中断されるため、完了を待つ（強制終了は Ctrl-C）。
			m.quitAfterOp = true
			m.note, m.noteErr = "実行中の操作の完了を待って終了します…（強制終了は Ctrl-C）", false
			return m, nil
		}
		return m, tea.Quit
	case "tab", "left", "right", "h", "l":
		m.toggleFocus()
		return m, nil
	case "!":
		return m.startOp("doctor", m.doctorCmd(false))
	}

	if m.focus == focusTree {
		return m.updateTreeKey(msg)
	}
	return m.updateLogKey(msg)
}

func (m *model) toggleFocus() {
	if m.focus == focusTree {
		m.focus = focusLog
	} else {
		m.focus = focusTree
	}
}

// startOp は統合操作を 1 つずつ実行するためのガード。実行中なら弾いて note を出す。
func (m *model) startOp(label string, cmd tea.Cmd) (tea.Model, tea.Cmd) {
	if m.opRunning {
		m.note, m.noteErr = "別の操作を実行中です", true
		return m, nil
	}
	m.opRunning = true
	m.opLabel = label
	m.note, m.noteErr = "", false
	return m, cmd
}

// updateFilterKey はフィルタ入力中のキー操作（ライブ反映）。
func (m *model) updateFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		m.prompt = promptNone
		m.filtering = false
		m.input.Blur()
	case tea.KeyEscape:
		m.prompt = promptNone
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

// updateCreateNameKey は worktree 名の入力。Enter で確定して作成先リポジトリの
// 選択モーダルへ進む（reposCmd）。
func (m *model) updateCreateNameKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		name := strings.TrimSpace(m.input.Value())
		if name == "" {
			m.note, m.noteErr = "worktree 名を入力してください", true
			return m, nil
		}
		m.createName = name
		m.prompt = promptNone
		m.input.Blur()
		return m, m.reposCmd()
	case tea.KeyEscape:
		m.prompt = promptNone
		m.input.Blur()
		return m, nil
	default:
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
}

// updateAliasKey は別名の入力。Enter で確定（空なら削除）、Esc で中止。
func (m *model) updateAliasKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		label := strings.TrimSpace(m.input.Value())
		name := m.promptTarget
		m.prompt = promptNone
		m.input.Blur()
		return m.startOp("alias "+name, m.aliasCmd(name, label))
	case tea.KeyEscape:
		m.prompt = promptNone
		m.input.Blur()
		return m, nil
	default:
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
}

// updateCreateReposKey は作成先リポジトリの選択モーダル。
func (m *model) updateCreateReposKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	n := 0
	if m.repos != nil {
		n = len(m.repos.Repos)
	}
	switch msg.String() {
	case "j", "down":
		if m.repoSel < n-1 {
			m.repoSel++
		}
	case "k", "up":
		if m.repoSel > 0 {
			m.repoSel--
		}
	case " ":
		if m.repoSel >= 0 && m.repoSel < len(m.repoChecked) {
			m.repoChecked[m.repoSel] = !m.repoChecked[m.repoSel]
		}
	case "a":
		// 全選択 / 全解除のトグル（1 つでも未選択なら全選択、そうでなければ全解除）。
		all := true
		for _, c := range m.repoChecked {
			if !c {
				all = false
				break
			}
		}
		for i := range m.repoChecked {
			m.repoChecked[i] = !all
		}
	case "enter":
		var repos []string
		for i, c := range m.repoChecked {
			if c {
				repos = append(repos, m.repos.Repos[i].Name)
			}
		}
		if len(repos) == 0 {
			m.note, m.noteErr = "リポジトリを 1 つ以上選択してください", true
			return m, nil
		}
		m.prompt = promptNone
		return m.startOp("create "+m.createName, m.createCmd(m.createName, repos))
	case "esc":
		m.prompt = promptNone
	}
	return m, nil
}

// updateConfirmRemoveKey は削除の確認。y のみ実行、それ以外は中止。
func (m *model) updateConfirmRemoveKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "y" {
		m.prompt = promptNone
		return m.startOp("remove "+m.promptTarget, m.removeCmd(m.promptTarget))
	}
	m.prompt = promptNone
	return m, nil
}

// updateDoctorKey は doctor 結果ペイン中のキー操作。q は終了ではなく結果を閉じる。
func (m *model) updateDoctorKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		m.doctorMode = false
		m.doctorText = nil
		m.rebuildLog()
	case "F":
		return m.startOp("doctor", m.doctorCmd(true))
	case "j", "down":
		m.vp.ScrollDown(1)
	case "k", "up":
		m.vp.ScrollUp(1)
	case "d":
		m.vp.HalfPageDown()
	case "u":
		m.vp.HalfPageUp()
	case "g":
		m.vp.GotoTop()
	case "G":
		m.vp.GotoBottom()
	}
	return m, nil
}

// updateTreeKey は左ペイン（ツリー）にフォーカスがあるときのキー操作。
func (m *model) updateTreeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "j", "down":
		return m, m.moveSel(1)
	case "k", "up":
		return m, m.moveSel(-1)
	case "enter", "s":
		if wt, ok := m.selectedWorktree(); ok {
			return m.startOp("switch "+wt, m.switchCmd(wt, false))
		}
	case "r":
		if wt, ok := m.selectedWorktree(); ok {
			return m.startOp("switch --restart "+wt, m.switchCmd(wt, true))
		}
	case "x":
		if wt, ok := m.selectedWorktree(); ok {
			return m.startOp("stop "+wt, m.stopCmd(wt))
		}
	case "R":
		return m, m.treesCmd()
	case "n":
		m.prompt = promptCreateName
		m.input.Prompt = "名前: "
		m.input.Placeholder = "worktree 名"
		m.input.SetValue("")
		m.input.Focus()
	case "D":
		if wt, ok := m.selectedWorktree(); ok {
			m.promptTarget = wt
			m.prompt = promptConfirmRemove
		}
	case "a":
		if wt, ok := m.selectedWorktree(); ok {
			m.promptTarget = wt
			m.prompt = promptAlias
			m.input.Prompt = "別名: "
			m.input.Placeholder = "表示用の別名（空で削除）"
			m.input.SetValue(m.selectedAlias())
			m.input.CursorEnd()
			m.input.Focus()
		}
	}
	return m, nil
}

// updateLogKey は右ペイン（ログ）にフォーカスがあるときのキー操作。
func (m *model) updateLogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "f":
		m.follow = !m.follow
		if m.follow {
			m.vp.GotoBottom()
		}
	case "/":
		m.prompt = promptFilter
		m.filtering = true
		m.input.Prompt = "/"
		m.input.Placeholder = "フィルタ（部分一致）"
		m.input.SetValue(m.filter)
		m.input.CursorEnd()
		m.input.Focus()
	case "p":
		m.prev = !m.prev
		// パスが変わる（.prev ⇔ 現行）ため即座に再解決する。tailer は
		// applyResolved のパス変化検出でリセットされる。
		return m, m.resolveCmd()
	case "w":
		m.wrap = !m.wrap
		m.rebuildLog()
	case "esc":
		if m.filter != "" {
			m.filter = ""
			m.input.SetValue("")
			m.rebuildLog()
		}
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

// moveSel はツリーのカーソルを delta 分動かす。サーバーノードに乗ったら表示ログ対象
// （curKey）を切り替え、worktree ノード上では対象を維持する。
func (m *model) moveSel(delta int) tea.Cmd {
	if len(m.nodes) == 0 {
		return nil
	}
	m.sel = clamp(m.sel+delta, 0, len(m.nodes)-1)
	n := m.nodes[m.sel]
	if !n.isWorktree() {
		if k := n.key(); k != m.curKey {
			m.curKey = k
			return m.selectTarget()
		}
	}
	return nil
}

// selectedWorktree はカーソル位置のノードが属する worktree 名を返す（worktree ノード・
// サーバーノードのどちらでも親の worktree 名になる）。
func (m *model) selectedWorktree() (string, bool) {
	if m.sel < 0 || m.sel >= len(m.nodes) {
		return "", false
	}
	return m.nodes[m.sel].wt, true
}

// selectedAlias はカーソル位置の worktree の現在の別名を返す（alias プロンプトの
// プリフィル用）。
func (m *model) selectedAlias() string {
	wt, ok := m.selectedWorktree()
	if !ok {
		return ""
	}
	for _, n := range m.nodes {
		if n.isWorktree() && n.wt == wt {
			return n.alias
		}
	}
	return ""
}

// buildNodes は trees・設定のサーバー定義・status から nodes を再構築する。各 worktree
// ノードの配下に、その worktree のメンバー repo に設定されたサーバー（∪ 稼働中に
// 現れる (repo, server)）を repo 名 → server 名の順で並べる。カーソル位置は可能なら
// 同じノードの key を保つ。
func (m *model) buildNodes() {
	prevKey := ""
	if m.sel >= 0 && m.sel < len(m.nodes) {
		prevKey = m.nodes[m.sel].key()
	}

	var nodes []node
	if m.trees != nil {
		// crashed 判定用: サーバーノードの key → クラッシュ。
		crashed := map[string]bool{}
		if m.status != nil {
			for _, r := range m.status.Rows {
				if r.State == server.StateCrashed && r.Worktree != "" {
					crashed[r.Worktree+"\x00"+r.Repo+"/"+r.Server] = true
				}
			}
		}
		// 設定上のサーバー定義（読めない設定はサーバーなし扱い）。
		var servers coreserver.Config
		if m.cfg != nil {
			servers = m.cfg.ServersConfig()
		}

		for _, wt := range m.trees.Worktrees {
			nodes = append(nodes, node{wt: wt.Name, alias: wt.Alias, broken: wt.Broken})

			type rs struct{ repo, server string }
			set := map[rs]bool{}
			for _, rc := range wt.Repos {
				if defs, ok := servers[rc.Repo]; ok {
					for _, name := range defs.SortedServerNames() {
						set[rs{rc.Repo, name}] = true
					}
				}
			}
			running := map[rs]int{}
			for _, sc := range wt.Servers {
				set[rs{sc.Repo, sc.Server}] = true
				running[rs{sc.Repo, sc.Server}] = sc.Pid
			}

			keys := make([]rs, 0, len(set))
			for k := range set {
				keys = append(keys, k)
			}
			sort.Slice(keys, func(i, j int) bool {
				if keys[i].repo != keys[j].repo {
					return keys[i].repo < keys[j].repo
				}
				return keys[i].server < keys[j].server
			})
			for _, k := range keys {
				pid, isRunning := running[k]
				nodes = append(nodes, node{
					wt:      wt.Name,
					repo:    k.repo,
					server:  k.server,
					running: isRunning,
					pid:     pid,
					crashed: crashed[wt.Name+"\x00"+k.repo+"/"+k.server],
				})
			}
		}
	}
	m.nodes = nodes

	// 旧カーソルのノードが残っていれば追従、消えていれば clamp。
	m.sel = 0
	for i, n := range nodes {
		if n.key() == prevKey {
			m.sel = i
			break
		}
	}
	m.sel = clamp(m.sel, 0, max(0, len(nodes)-1))
}

// ensureSelection は curKey を有効なサーバーノードに保つ。消えた対象はプレースホルダへ
// 戻し、未選択（初回など）なら最初のサーバーノードへ合わせて再解決する。
func (m *model) ensureSelection() tea.Cmd {
	if m.curKey != "" && !m.hasNode(m.curKey) {
		// 対象が消えたら表示をプレースホルダへ戻す。
		m.curKey = ""
	}
	if m.curKey == "" {
		if k, ok := m.firstServerKey(); ok {
			m.curKey = k
			return m.selectTarget()
		}
	}
	return nil
}

func (m *model) hasNode(key string) bool {
	for _, n := range m.nodes {
		if n.key() == key {
			return true
		}
	}
	return false
}

func (m *model) firstServerKey() (string, bool) {
	for _, n := range m.nodes {
		if !n.isWorktree() {
			return n.key(), true
		}
	}
	return "", false
}

// selectTarget は curKey を切り替えた直後の処理。既存バッファがあれば過去分を即座に
// 表示し、いずれにせよ再解決でパスを取り直す。
func (m *model) selectTarget() tea.Cmd {
	m.follow = true
	m.curReadErr = ""
	if _, ok := m.bufs[m.curKey]; !ok {
		// まだバッファが無い対象はパス解決までプレースホルダを出す。
		m.curPath = ""
		m.curMissing = false
	}
	m.rebuildLog()
	return m.resolveCmd()
}

// applyResolved は再解決の結果（設定・状態・選択対象のログパス）をモデルへ写す。
// selKey が現在の curKey と一致するときだけログ対象を更新する（発行から到着までに
// 選択が動いた古い結果を無視するための照合）。パスが変わった対象（外部の switch や
// --prev トグル）は tailer とバッファを作り直し、新しいログを末尾から読み直す。
func (m *model) applyResolved(msg resolvedMsg) {
	if msg.err != nil {
		m.note, m.noteErr = "再解決に失敗: "+msg.err.Error(), true
		return
	}
	if msg.warn != "" {
		m.note, m.noteErr = msg.warn, true
	}
	if msg.cfg != nil {
		m.cfg = msg.cfg
	}
	m.status = msg.status
	m.buildNodes()

	if msg.selKey == m.curKey && m.curKey != "" {
		switch {
		case msg.missing:
			// ログ未生成: 追跡をやめてプレースホルダを出す（期待パスは保持）。
			m.curMissing = true
			m.curPath = msg.path
			delete(m.tails, m.curKey)
			delete(m.bufs, m.curKey)
		case msg.path != "":
			m.curMissing = false
			if msg.path != m.curPath || m.tails[m.curKey] == nil {
				m.curPath = msg.path
				m.tails[m.curKey] = newTailer(msg.path)
				m.bufs[m.curKey] = newRing(targetRingCap)
				m.curReadErr = ""
			}
			m.pollTail()
		}
	}
	m.rebuildLog()
}

// pollTail は選択中の対象 1 本だけを増分読みし、バッファへ追記する。追記があったかを
// 返す（全対象を毎ティック読むのは廃止 — 表示しているログのみ追う）。
func (m *model) pollTail() bool {
	if m.curKey == "" {
		return false
	}
	tail := m.tails[m.curKey]
	buf := m.bufs[m.curKey]
	if tail == nil || buf == nil {
		return false
	}
	lines, err := tail.poll()
	if err != nil {
		m.curReadErr = err.Error()
		return false
	}
	m.curReadErr = ""
	if len(lines) == 0 {
		return false
	}
	buf.push(lines...)
	return true
}

// rebuildLog は現在の選択・フィルタ（または doctor 結果）からビューポートの内容を
// 組み立て直す。
func (m *model) rebuildLog() {
	if !m.ready {
		return
	}
	if m.doctorMode {
		lines := m.doctorText
		if m.wrap {
			wrapped := make([]string, len(lines))
			for i, l := range lines {
				wrapped[i] = wrapDisplay(l, m.vp.Width)
			}
			lines = wrapped
		}
		m.vp.SetContent(strings.Join(lines, "\n"))
		return
	}
	if m.curKey == "" {
		m.vp.SetContent("サーバーノードを選択してください")
		return
	}
	buf := m.bufs[m.curKey]
	if buf == nil {
		if m.curMissing {
			m.vp.SetContent("ログがまだありません (" + m.curPath + ")")
		} else {
			m.vp.SetContent("ログを解決中…")
		}
		return
	}

	filter := strings.ToLower(m.filter)
	src := buf.slice()
	rendered := make([]string, 0, len(src))
	for _, text := range src {
		if filter != "" && !strings.Contains(strings.ToLower(text), filter) {
			continue
		}
		rendered = append(rendered, colorizeLog(text))
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

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
