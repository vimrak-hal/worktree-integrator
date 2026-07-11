package tui

import (
	"context"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/help"
	kb "github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

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
)

// バッファの保持行数と描画上限。単一サーバーのログだけを表示するため、マージ用の
// 大きなリングは持たない。
const (
	targetRingCap = 4000
	// maxRender はビューポートへ流す最大行数。フィルタ後にこれを超える分は古い側を
	// 落とす（描画コストの上限）。
	maxRender = 2000
	// maxEvents はイベント履歴の保持上限。古い側から捨ててメモリを一定に保つ。
	maxEvents = 100
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

	// collapsed は worktree ノードが折りたたみ表示か（サーバーノードでは常に false）。
	// 折りたたみ中は配下のサーバーノードを生成せず、見出しに集約マークを付ける。
	collapsed bool
	// nRunning / nCrashed / nStopped は折りたたみ見出しの集約表示に使う、配下サーバーの
	// 状態別件数（worktree ノードのみ設定）。折りたたみの有無に依らず配下の全数を数える。
	nRunning, nCrashed, nStopped int
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

	// keys は全キーバインド、help はヘルプ行の描画器。キー処理は keys と
	// kb.Matches で照合し、ヘルプ行は contextBindings を help が描く。
	keys keyMap
	help help.Model

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
	// collapsed は worktree 名 → ユーザーの明示的な折りたたみ指定。**キーが存在しない
	// 場合は既定ルールに従う**（稼働中またはクラッシュのサーバーが 1 つでもあれば展開、
	// すべて停止なら折りたたみ）。明示指定は両方向とも既定ルールに優先する。worktree が
	// 消えたら buildNodes の掃除で該当キーも落とす。
	collapsed map[string]bool
	// allServerKeys は直近の buildNodes が算出した「worktree × 設定上のサーバー定義」の
	// 全体集合（折りたたみ非依存）。折りたたみで m.nodes からサーバーノードが消えても、
	// tails/bufs の掃除・ensureSelection の curKey 有効性判定はこの集合で行う。
	allServerKeys map[string]bool

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
	// resolveSeq は resolveCmd の発行カウンタ、resolveApplied は適用済みの最大世代。
	// selKey 照合はカーソル移動しか弾けず、ロック競合で遅延した古い解決が新しい解決を
	// 追い越すと path が巻き戻るため、発行順の世代で古い結果を捨てる（B2）。
	resolveSeq     uint64
	resolveApplied uint64

	// --- モーダル・結果ペイン ---
	prompt promptMode
	// promptTarget は alias / remove フォームの対象 worktree 名（フォームを開いた
	// 時点のカーソル位置を固定して保持する）。
	promptTarget string
	repos        *app.ReposResult

	// --- huh フォーム（作成・別名・削除確認） ---
	// form が非 nil の間はキー入力をフォームが最優先で消費する。formKind はどの
	// フォームかを表し、完了時に finishForm が値を取り出して dispatch する。値は
	// 各 form* フィールドへポインタでバインドされる。
	form        *huh.Form
	formKind    formKind
	formName    string
	formRepos   []string
	formAlias   string
	formConfirm bool
	// doctorText / doctorMode は doctor 結果の右ペイン表示。doctorMode 中は vp の
	// 内容が doctorText になる（rebuildLog が出し分ける）。
	doctorText []string
	doctorMode bool

	// opRunning 中は新しい統合操作を受け付けない（1 つずつ実行する）。
	opRunning bool
	opLabel   string
	// quitAfterOp は操作中に終了要求があったことを表す（完了を待って終了する）。
	quitAfterOp bool
	// events はライフサイクル・作成進捗のイベント履歴。フッターには直近
	// visibleEvents 件を時系列（上が古い順）で表示する。
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
		ctx:       ctx,
		root:      root,
		cfg:       cfg,
		fw:        fw,
		keys:      newKeyMap(),
		help:      newHelp(),
		focus:     focusTree,
		follow:    true,
		wrap:      true,
		input:     input,
		tails:     map[string]*tailer{},
		bufs:      map[string]*ring{},
		collapsed: map[string]bool{},
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

// rightW は右ペイン（ログ・フォーム）の表示幅。左ペインと区切り "│" を除いた残り。
// ビューポート幅（Update の WindowSizeMsg で決まる幅）と同じ計算で、huh フォームの
// 幅指定にも使う。
func (m *model) rightW() int {
	return max(1, m.width-m.leftW()-1)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// ヘルプ行の幅超過時の省略（…）を端末幅に合わせる。
		m.help.Width = m.width
		lw := m.leftW()
		// クローム 3 行（ペインタイトル行・note 行・ヘルプ行）を除いた残りが本文。
		w, h := max(1, m.width-lw-1), max(1, m.height-3)
		if !m.ready {
			m.vp = viewport.New(w, h)
			m.ready = true
		} else {
			// 作り直すと YOffset が 0 に戻り、追従オフで過去ログを読んでいる最中の
			// リサイズで先頭へ飛ぶ。viewport は派生状態を都度計算するので、
			// 幅・高さの直接代入で YOffset を保ったまま更新できる。
			m.vp.Width = w
			m.vp.Height = h
		}
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
		if m.form != nil || m.opRunning {
			// 別フォーム表示中・操作実行中に届いた候補は古い（または重複した）要求のもの。
			// 開いているフォームを作成フォームで潰さないよう黙って破棄する。
			return m, nil
		}
		if msg.err != nil {
			m.note, m.noteErr = "リポジトリ一覧の取得に失敗: "+msg.err.Error(), true
			return m, nil
		}
		m.repos = msg.res
		m.formName = ""
		m.formRepos = nil
		m.form = newCreateForm(msg.res.Repos, &m.formName, &m.formRepos, m.rightW())
		m.formKind = formCreate
		return m, m.form.Init()

	case eventMsg:
		m.events = append(m.events, msg.line)
		if len(m.events) > maxEvents {
			m.events = m.events[len(m.events)-maxEvents:]
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
			// 直前のログ閲覧の YOffset を引き継ぐと、長い診断結果が途中／末尾から
			// 表示される。診断は必ず先頭から読ませる。
			m.vp.GotoTop()
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
	// huh フォームは Enter などのキーを内部メッセージ（次フィールドへ・確定）に変換し、
	// コマンド経由で自分宛てに送り返してくる。KeyMsg 以外のそれらもフォームへ届けないと
	// 確定が永遠に完了しない。
	if m.form != nil {
		return m.updateForm(msg)
	}
	return m, nil
}

// updateKey はキー入力をさばく。モーダル（プロンプト）と結果ペイン（doctor）を
// 最優先で処理し、その後にグローバル・ペイン別のキーへ落とす。
func (m *model) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}

	// huh フォーム表示中は Ctrl-C 以外の全キーをフォームが消費する。ただし Esc は
	// この層で横取りする: MultiSelect のフィルタ入力中だけは huh に渡し（フィルタ
	// 確定/解除）、それ以外は TUI がフォーム中止として畳む。huh の KeyMap.Quit に esc を
	// 足す方式だと、フィルタ入力中の Esc までフォーム全体の中止に化けて入力済みの内容が
	// 消えるため、フォーカス中フィールドの状態をここで判定する。
	if m.form != nil {
		if msg.Type == tea.KeyEsc && !formFiltering(m.form) {
			m.form = nil
			m.formKind = formNone
			return m, nil
		}
		return m.updateForm(msg)
	}

	if m.prompt == promptFilter {
		return m.updateFilterKey(msg)
	}

	if m.doctorMode {
		return m.updateDoctorKey(msg)
	}

	switch {
	case kb.Matches(msg, m.keys.Quit):
		if m.opRunning {
			// 操作の途中で抜けると中断されるため、完了を待つ（強制終了は Ctrl-C）。
			m.quitAfterOp = true
			m.note, m.noteErr = "実行中の操作の完了を待って終了します…（強制終了は Ctrl-C）", false
			return m, nil
		}
		return m, tea.Quit
	case kb.Matches(msg, m.keys.Focus):
		m.toggleFocus()
		return m, nil
	case kb.Matches(msg, m.keys.Doctor):
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

// updateDoctorKey は doctor 結果ペイン中のキー操作。q は終了ではなく結果を閉じる。
func (m *model) updateDoctorKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case kb.Matches(msg, m.keys.Cancel), kb.Matches(msg, m.keys.Quit):
		m.doctorMode = false
		m.doctorText = nil
		m.rebuildLog()
	case kb.Matches(msg, m.keys.Fix):
		return m.startOp("doctor", m.doctorCmd(true))
	case kb.Matches(msg, m.keys.LineDown):
		m.vp.ScrollDown(1)
	case kb.Matches(msg, m.keys.LineUp):
		m.vp.ScrollUp(1)
	case kb.Matches(msg, m.keys.HalfDown):
		m.vp.HalfPageDown()
	case kb.Matches(msg, m.keys.HalfUp):
		m.vp.HalfPageUp()
	case kb.Matches(msg, m.keys.Top):
		m.vp.GotoTop()
	case kb.Matches(msg, m.keys.Bottom):
		m.vp.GotoBottom()
	}
	return m, nil
}

// updateTreeKey は左ペイン（ツリー）にフォーカスがあるときのキー操作。
func (m *model) updateTreeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case kb.Matches(msg, m.keys.Down):
		return m, m.moveSel(1)
	case kb.Matches(msg, m.keys.Up):
		return m, m.moveSel(-1)
	case kb.Matches(msg, m.keys.NextWorktree):
		m.jumpWorktree(1)
		return m, nil
	case kb.Matches(msg, m.keys.PrevWorktree):
		m.jumpWorktree(-1)
		return m, nil
	case kb.Matches(msg, m.keys.Collapse):
		m.toggleCollapse()
		return m, nil
	case kb.Matches(msg, m.keys.SwitchTo):
		if wt, ok := m.selectedWorktree(); ok {
			return m.startOp("switch "+wt, m.switchCmd(wt, false))
		}
	case kb.Matches(msg, m.keys.Restart):
		if wt, ok := m.selectedWorktree(); ok {
			return m.startOp("switch --restart "+wt, m.switchCmd(wt, true))
		}
	case kb.Matches(msg, m.keys.Stop):
		if wt, ok := m.selectedWorktree(); ok {
			return m.startOp("stop "+wt, m.stopCmd(wt))
		}
	case kb.Matches(msg, m.keys.Refresh):
		return m, m.treesCmd()
	case kb.Matches(msg, m.keys.New):
		if m.opRunning {
			// フォームを入力し終えてから弾かれる無駄を防ぐため、候補取得の発行前に
			// opRunning を弾く。R（treesCmd）は読み取り専用なのでガード不要。
			m.note, m.noteErr = "別の操作を実行中です", true
			return m, nil
		}
		// reposMsg 受信で作成フォームを開く（名前とリポジトリ選択は 1 枚のフォームに
		// 統合されている）。
		return m, m.reposCmd()
	case kb.Matches(msg, m.keys.Delete):
		if wt, ok := m.selectedWorktree(); ok {
			m.promptTarget = wt
			m.formConfirm = false
			m.form = newRemoveForm(wt, &m.formConfirm, m.rightW())
			m.formKind = formRemove
			return m, m.form.Init()
		}
	case kb.Matches(msg, m.keys.Alias):
		if wt, ok := m.selectedWorktree(); ok {
			m.promptTarget = wt
			m.formAlias = m.selectedAlias()
			m.form = newAliasForm(&m.formAlias, m.rightW())
			m.formKind = formAlias
			return m, m.form.Init()
		}
	}
	return m, nil
}

// updateLogKey は右ペイン（ログ）にフォーカスがあるときのキー操作。
func (m *model) updateLogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case kb.Matches(msg, m.keys.Follow):
		m.follow = !m.follow
		if m.follow {
			m.vp.GotoBottom()
		}
	case kb.Matches(msg, m.keys.Filter):
		m.prompt = promptFilter
		m.filtering = true
		m.input.Prompt = "/"
		m.input.Placeholder = "フィルタ（部分一致）"
		m.input.SetValue(m.filter)
		m.input.CursorEnd()
		m.input.Focus()
	case kb.Matches(msg, m.keys.Prev):
		m.prev = !m.prev
		// パスが変わる（.prev ⇔ 現行）ため即座に再解決する。tailer は
		// applyResolved のパス変化検出でリセットされる。
		return m, m.resolveCmd()
	case kb.Matches(msg, m.keys.Wrap):
		m.wrap = !m.wrap
		m.rebuildLog()
	case kb.Matches(msg, m.keys.ClearFilter):
		if m.filter != "" {
			m.filter = ""
			m.input.SetValue("")
			m.rebuildLog()
		}
	case kb.Matches(msg, m.keys.Top):
		m.follow = false
		m.vp.GotoTop()
	case kb.Matches(msg, m.keys.Bottom):
		m.vp.GotoBottom()
	case kb.Matches(msg, m.keys.LineDown):
		m.follow = false
		m.vp.ScrollDown(1)
	case kb.Matches(msg, m.keys.LineUp):
		m.follow = false
		m.vp.ScrollUp(1)
	case kb.Matches(msg, m.keys.HalfDown):
		m.follow = false
		m.vp.HalfPageDown()
	case kb.Matches(msg, m.keys.HalfUp):
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

// isCollapsed は worktree の折りたたみ状態を決める。不変条件: ユーザーの明示指定
// （m.collapsed にキーが存在する）は両方向とも既定ルールに優先する。指定が無ければ
// 既定ルール（anyActive=稼働中またはクラッシュが 1 つでもある → 展開、全停止 → 折りたたみ）。
func (m *model) isCollapsed(wt string, anyActive bool) bool {
	if v, ok := m.collapsed[wt]; ok {
		return v
	}
	return !anyActive
}

// toggleCollapse はカーソル位置の worktree（サーバーノード上ならその親 worktree）の
// 折りたたみをトグルし、明示指定として m.collapsed に記録する。現在の実効状態は直近の
// buildNodes が見出しノードへ書いた collapsed を正として反転するため、既定ルールで
// 折りたたまれている worktree でも Space 一発で展開できる（明示展開が既定ルールに勝つ）。
// サーバーノード上から折りたたんだ場合は、消えるサーバーノードにカーソルが取り残されない
// よう見出しへ移す。
func (m *model) toggleCollapse() {
	wt, ok := m.selectedWorktree()
	if !ok {
		return
	}
	onServer := !m.nodes[m.sel].isWorktree()
	m.collapsed[wt] = !m.worktreeCollapsed(wt)
	m.buildNodes()
	if onServer && m.collapsed[wt] {
		m.selectWorktreeHeading(wt)
	}
}

// worktreeCollapsed は現在の見出しノードが持つ実効的な折りたたみ状態を返す（明示指定と
// 既定ルールを buildNodes が解決済みの値）。見出しが無ければ false。
func (m *model) worktreeCollapsed(wt string) bool {
	for _, n := range m.nodes {
		if n.isWorktree() && n.wt == wt {
			return n.collapsed
		}
	}
	return false
}

// selectWorktreeHeading はカーソルを指定 worktree の見出しノードへ移す。
func (m *model) selectWorktreeHeading(wt string) {
	for i, n := range m.nodes {
		if n.isWorktree() && n.wt == wt {
			m.sel = i
			return
		}
	}
}

// jumpWorktree はカーソルを次（dir=1）／前（dir=-1）の worktree 見出しノードへ移す。
// 間のサーバーノードは飛ばし、端ではラップせず（見つからなければ）動かない。
func (m *model) jumpWorktree(dir int) {
	for i := m.sel + dir; i >= 0 && i < len(m.nodes); i += dir {
		if m.nodes[i].isWorktree() {
			m.sel = i
			return
		}
	}
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
	// allKeys は「worktree × 設定上のサーバー定義（∪ 稼働中に現れる座標）」の全体集合。
	// 折りたたみに依存せず（折りたたんだ worktree のサーバーもここに数える）、tails/bufs の
	// 掃除と ensureSelection の curKey 有効性判定はこの集合で行う。折りたたみでバッファや
	// ログ対象が消えてはいけない、という不変条件のための土台。
	allKeys := map[string]bool{}
	// liveWts は現存する worktree 名の集合。明示指定マップ（m.collapsed）の掃除に使う。
	liveWts := map[string]bool{}
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
			liveWts[wt.Name] = true

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

			// 配下サーバーノードを先に組み、状態別に数える（集約表示・既定ルールの判定用）。
			// allKeys へは折りたたみに依らず全サーバーの key を登録する。
			srvNodes := make([]node, 0, len(keys))
			var nRunning, nCrashed, nStopped int
			for _, k := range keys {
				pid, isRunning := running[k]
				sn := node{
					wt:      wt.Name,
					repo:    k.repo,
					server:  k.server,
					running: isRunning,
					pid:     pid,
					crashed: crashed[wt.Name+"\x00"+k.repo+"/"+k.server],
				}
				allKeys[sn.key()] = true
				switch {
				case sn.running:
					nRunning++
				case sn.crashed:
					nCrashed++
				default:
					nStopped++
				}
				srvNodes = append(srvNodes, sn)
			}

			// 既定ルール: 稼働中またはクラッシュが 1 つでもあれば展開、全停止なら折りたたみ。
			// 明示指定（両方向）が常に優先する。
			collapsed := m.isCollapsed(wt.Name, nRunning > 0 || nCrashed > 0)
			nodes = append(nodes, node{
				wt: wt.Name, alias: wt.Alias, broken: wt.Broken,
				collapsed: collapsed,
				nRunning:  nRunning, nCrashed: nCrashed, nStopped: nStopped,
			})
			// 折りたたまれた worktree はサーバーノードを生成しない（見出しのみ）。
			if !collapsed {
				nodes = append(nodes, srvNodes...)
			}
		}
	}
	m.nodes = nodes
	m.allServerKeys = allKeys

	// 旧カーソルのノードが残っていれば追従、消えていれば clamp。
	m.sel = 0
	for i, n := range nodes {
		if n.key() == prevKey {
			m.sel = i
			break
		}
	}
	m.sel = clamp(m.sel, 0, max(0, len(nodes)-1))

	// worktree の作成→削除を繰り返すと key が毎回変わり、掃除しないとリング
	// （最大 targetRingCap 行）が対象ごとに残ってヒープが単調増加する。掃除・curKey の
	// 有効性判定はどちらも allKeys（折りたたみ非依存の全体集合）で行う: 折りたたんだ
	// worktree のサーバーもバッファ・ログ対象を維持し、折りたたみでログが消えないため。
	// tailer は poll ごとに open/close なので fd の後始末は不要。curKey は ensureSelection で
	// ノード集合内へ戻るが、表示中の対象は防御的に保護する。
	for k := range m.tails {
		if !allKeys[k] && k != m.curKey {
			delete(m.tails, k)
		}
	}
	for k := range m.bufs {
		if !allKeys[k] && k != m.curKey {
			delete(m.bufs, k)
		}
	}
	// 明示的な折りたたみ指定も、worktree が消えたら同じ場所で掃除する。
	for wt := range m.collapsed {
		if !liveWts[wt] {
			delete(m.collapsed, wt)
		}
	}
}

// ensureSelection は curKey を有効なサーバーノードに保つ。消えた対象はプレースホルダへ
// 戻し、未選択（初回など）なら最初のサーバーノードへ合わせて再解決する。
func (m *model) ensureSelection() tea.Cmd {
	// 有効性判定は m.nodes ではなく allServerKeys（折りたたみ非依存の全体集合）で行う。
	// 折りたたんだ worktree のサーバーノードは m.nodes に無いが、ログ対象としては生きて
	// いる（「サーバーノード上に無い間は直近の対象を表示」という既存セマンティクスを維持）。
	if m.curKey != "" && !m.allServerKeys[m.curKey] {
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
	// selKey 照合はカーソル移動しか弾けない。switch 直後などロック競合で遅延した古い
	// 解決が新しい解決を追い越すと、missing がバッファを消し path が巻き戻るため、
	// 発行順の世代（seq）で古い解決を捨てる。等号は許容（同一 seq は来ない設計だが
	// 安全側に倒す）。
	if msg.seq < m.resolveApplied {
		return
	}
	m.resolveApplied = msg.seq
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
