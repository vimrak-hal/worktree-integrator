package tui

import (
	"context"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/statedir"
)

// model は TUI 全体の状態。Bubble Tea の Elm アーキテクチャに乗り、更新は
// Update・描画は View に閉じる。状態は 5 つのサブモデル（ツリー・ログ・doctor・
// フォーム・操作状態）へ分割し、ルートはメッセージのルーティングとサブモデル間の
// 調停（ツリー→ログの掃除・カーソル移動での対象切替）だけを持つ。
type model struct {
	// ops は統合操作・読み取りの実行を担う継ぎ目（本番は appOps）。キー入力から発行される
	// 操作と引数の結線はテストがフェイクを差し込んで検証する。ctx / root / fw はモデルの
	// 生存期間を通じて不変なため、モデルには持たず ops（appOps）に閉じる。
	ops runner
	// cfg は直近に正常に読み込めた設定。MCP サーバーと同様に定期的に再読み込みし、
	// 編集は TUI の再起動なしで反映される（読めない間は直近の正常値で動き続ける）。
	cfg *config.File

	// keys は全キーバインド、help はヘルプ行の描画器。キー処理は keys と
	// kb.Matches で照合し、ヘルプ行は contextBindings を help が描く。
	keys keyMap
	help help.Model

	width, height int

	// tree はツリー（左ペイン）、log はログ（右ペイン）、doctor は doctor 結果ダイアログ、
	// forms は huh フォーム（作成・別名・削除確認）、op は統合操作の実行状態・イベント。
	tree   treeModel
	log    logModel
	doctor doctorModel
	forms  formController
	op     opState
}

func newModel(ctx context.Context, cfg *config.File, root statedir.Root, fw *forwarder) *model {
	input := textinput.New()
	input.Prompt = "/"
	input.Placeholder = "フィルタ（部分一致）"
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sp.Style = lipgloss.NewStyle().Foreground(colorAccent)
	return &model{
		ops:  appOps{ctx: ctx, root: root, fw: fw},
		cfg:  cfg,
		keys: newKeyMap(),
		help: newHelp(),
		tree: treeModel{collapsed: map[string]bool{}},
		log: logModel{
			follow: true,
			wrap:   true,
			input:  input,
			tails:  map[string]*tailer{},
			bufs:   map[string]*ring{},
		},
		op: opState{spin: sp},
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

// dialogOuterW はフローティングダイアログ（フォーム）の外枠幅。幅 = min(64, width-6)。
// 超狭小端末では負値・0 を 1 に丸めて panic を避ける（描画は borderTop が素の上辺に落ちる）。
func (m *model) dialogOuterW() int {
	w := min(64, m.width-6)
	if w < 1 {
		return 1
	}
	return w
}

// dialogInnerW はダイアログのボーダー内側幅（huh フォームの WithWidth と同じ）。
func (m *model) dialogInnerW() int { return max(1, m.dialogOuterW()-2) }

// dialogFormH は huh フォームの WithHeight の上限。画面より大きくならないようにするための
// 天井であり、実際のダイアログ高さは描画時にフォーム内容へ合わせて切り詰める（末尾の空行を
// 落とす）。
func (m *model) dialogFormH() int { return max(3, m.height-6) }

// doctorOuterW / doctorInnerW / doctorInnerH は doctor 結果ダイアログの寸法。幅 =
// min(90, width-6)、内側高さ ≒ height-6-2（外枠 height-6 から上下ボーダーを引く）。
func (m *model) doctorOuterW() int {
	w := min(90, m.width-6)
	if w < 1 {
		return 1
	}
	return w
}

func (m *model) doctorInnerW() int { return max(1, m.doctorOuterW()-2) }
func (m *model) doctorInnerH() int { return max(1, m.height-8) }

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// ヘルプ行の幅超過時の省略（…）を端末幅に合わせる。
		m.help.Width = m.width
		lw := m.leftW()
		// 右ペインの内側寸法。左ボックス幅 lw と右ボーダー 2 桁（左右）を引いた幅、
		// 上下ボーダー 2 行 + ヘルプ行 1 行を引いた高さ（= ビューポートの寸法）。
		w, h := max(1, m.width-lw-2), max(1, m.height-3)
		m.log.resize(w, h)
		// doctor ダイアログ表示中はその専用ビューポートも追従させ、内容を再折り返しする。
		if m.doctor.doctorMode {
			m.doctor.resize(m.doctorInnerW(), m.doctorInnerH())
		}
		m.log.rebuild()
		return m, nil

	case tailTickMsg:
		if m.log.pollTail() {
			m.log.rebuild()
		}
		return m, tailTick()

	case resolveTickMsg:
		return m, tea.Batch(m.resolveCmd(), resolveTick())

	case spinner.TickMsg:
		// 実行中の間だけスピナーを進める。opRunning が下りたら転送せず tick を止め、
		// 無駄な再描画を避ける（startOp が次の実行開始時に改めて回し始める）。
		if !m.op.opRunning {
			return m, nil
		}
		var cmd tea.Cmd
		m.op.spin, cmd = m.op.spin.Update(msg)
		return m, cmd

	case treesTickMsg:
		// 操作の実行中は worktree 一覧を触らない（完了時に取り直す）。
		if !m.op.opRunning {
			return m, tea.Batch(m.treesCmd(), treesTick())
		}
		return m, treesTick()

	case resolvedMsg:
		m.applyResolved(msg)
		return m, nil

	case treesMsg:
		if msg.err != nil {
			m.tree.treesErr = msg.err.Error()
		} else {
			m.tree.treesErr = ""
			m.tree.trees = msg.res
		}
		m.buildNodes()
		if cmd := m.ensureSelection(); cmd != nil {
			return m, cmd
		}
		m.log.rebuild()
		return m, nil

	case reposMsg:
		if m.forms.form != nil || m.op.opRunning {
			// 別フォーム表示中・操作実行中に届いた候補は古い（または重複した）要求のもの。
			// 開いているフォームを作成フォームで潰さないよう黙って破棄する。
			return m, nil
		}
		if msg.err != nil {
			m.op.note, m.op.noteErr = "リポジトリ一覧の取得に失敗: "+msg.err.Error(), true
			return m, nil
		}
		return m, m.forms.openCreate(msg.res, m.dialogInnerW(), m.dialogFormH())

	case eventMsg:
		m.op.addEvent(msg.line)
		return m, nil

	case opDoneMsg:
		m.op.finish(msg.summary, msg.err)
		if len(msg.doctorText) > 0 {
			// doctor は専用ダイアログ（dvp）で表示する。背後の右ペインはログのまま。
			// 長い診断結果は必ず先頭から読ませる。
			m.doctor.show(msg.doctorText, m.doctorInnerW(), m.doctorInnerH())
		}
		if m.op.quitAfterOp {
			return m, tea.Quit
		}
		// 切り替え・停止・作成・削除で状態・一覧・ログパスが変わったはずなので、
		// 即座に再解決する。
		return m, tea.Batch(m.resolveCmd(), m.treesCmd())

	case tea.MouseMsg:
		// ダイアログ表示中はホイールをそのダイアログへ。doctor はスクロール、フォームは無視。
		if m.doctor.doctorMode {
			var cmd tea.Cmd
			m.doctor.dvp, cmd = m.doctor.dvp.Update(msg)
			return m, cmd
		}
		if m.forms.form != nil {
			return m, nil
		}
		// ダイアログ非表示中はホイールでログをスクロールする。上方向は追従を解除。
		if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonWheelUp {
			m.log.follow = false
		}
		var cmd tea.Cmd
		m.log.vp, cmd = m.log.vp.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		return m.updateKey(msg)
	}
	// huh フォームは Enter などのキーを内部メッセージ（次フィールドへ・確定）に変換し、
	// コマンド経由で自分宛てに送り返してくる。KeyMsg 以外のそれらもフォームへ届けないと
	// 確定が永遠に完了しない。
	if m.forms.form != nil {
		return m.updateForm(msg)
	}
	return m, nil
}

// startOp は opState.startOp のルート薄ラッパ。統合操作を 1 つずつ実行するためのガードで、
// 実行中なら弾いて note を出す。キーハンドラが (tea.Model, tea.Cmd) を返せるようここで包む。
func (m *model) startOp(label string, cmd tea.Cmd) (tea.Model, tea.Cmd) {
	return m, m.op.startOp(label, cmd)
}

// buildNodes はツリー（左ペイン）を再構築し、続けてログ側の掃除を行うルートの調停
// メソッド。ツリーとログの結合点の 1 つ: buildNodes 後に allServerKeys（折りたたみ
// 非依存の全体集合）で tails/bufs を掃除する。掃除・curKey の有効性判定を折りたたみ
// 非依存の集合で行うことで、「折りたたんでもバッファ・ログ対象が消えない」不変条件を
// 保つ。表示中の対象（curKey）は防御的に常に残す。tailer は poll ごとに open/close
// なので fd の後始末は不要。
func (m *model) buildNodes() {
	m.tree.buildNodes(m.cfg)
	// worktree の作成→削除を繰り返すと key が毎回変わり、掃除しないとリング（最大
	// targetRingCap 行）が対象ごとに残ってヒープが単調増加する。
	m.log.prune(m.tree.allServerKeys)
}

// moveSel はツリーのカーソルを delta 分動かすルートの調停メソッド。ツリーとログの
// 結合点の 1 つ: サーバーノードに乗ったら表示ログ対象（curKey）を切り替え、worktree
// ノード上では対象を維持する。
func (m *model) moveSel(delta int) tea.Cmd {
	if k, onServer := m.tree.moveSel(delta); onServer && k != m.log.curKey {
		m.log.curKey = k
		return m.selectTarget()
	}
	return nil
}

// ensureSelection は curKey を有効なサーバーノードに保つルートの調停メソッド。消えた
// 対象はプレースホルダへ戻し、未選択（初回など）なら最初のサーバーノードへ合わせて
// 再解決する。有効性判定は m.tree.nodes ではなく allServerKeys（折りたたみ非依存の全体
// 集合）で行う: 折りたたんだ worktree のサーバーノードは nodes に無いが、ログ対象として
// は生きている（「サーバーノード上に無い間は直近の対象を表示」という既存セマンティクスを維持）。
func (m *model) ensureSelection() tea.Cmd {
	if m.log.curKey != "" && !m.tree.allServerKeys[m.log.curKey] {
		// 対象が消えたら表示をプレースホルダへ戻す。
		m.log.curKey = ""
	}
	if m.log.curKey == "" {
		if k, ok := m.tree.firstServerKey(); ok {
			m.log.curKey = k
			return m.selectTarget()
		}
	}
	return nil
}

// selectTarget は curKey を切り替えた直後の処理。ログ側のプレースホルダ準備を済ませ、
// いずれにせよ再解決でパスを取り直す（resolveCmd は cfg / ops に触れるためルートに残す）。
func (m *model) selectTarget() tea.Cmd {
	m.log.prepareTarget()
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
	if msg.seq < m.log.resolveApplied {
		return
	}
	m.log.resolveApplied = msg.seq
	if msg.err != nil {
		m.op.note, m.op.noteErr = "再解決に失敗: "+msg.err.Error(), true
		return
	}
	if msg.warn != "" {
		m.op.note, m.op.noteErr = msg.warn, true
	}
	if msg.cfg != nil {
		m.cfg = msg.cfg
	}
	m.tree.status = msg.status
	m.buildNodes()
	m.log.applyPath(msg)
	m.log.rebuild()
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
