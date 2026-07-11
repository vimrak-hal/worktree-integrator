package tui

import (
	"context"
	"sort"

	"github.com/charmbracelet/bubbles/help"
	kb "github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/vimrak-hal/worktree-integrator/internal/app/server"
	"github.com/vimrak-hal/worktree-integrator/internal/app/tree"
	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
	coreserver "github.com/vimrak-hal/worktree-integrator/internal/core/server"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/statedir"
)

// focusID はキー入力を受けるペイン。左（ツリー）と右（ログ）の 2 ペインを表し、
// ペイン見出しはフォーカス側を反転で強調する。
type focusID int

const (
	focusTree focusID = iota
	focusLog
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
// よう検証済み、wt は '/' を含みうるが "\x00" 区切りで一意）。
func (n node) key() string {
	if n.isWorktree() {
		return n.wt
	}
	return n.wt + "\x00" + n.repo + "/" + n.server
}

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

	// --- ツリー（左ペイン） ---
	trees    *tree.ListResult
	treesErr string
	status   *server.StatusResult
	nodes    []node
	// sel はツリーのカーソル位置（nodes のインデックス）。
	sel int
	// treeTop はツリーの縦スクロール位置（可視域の先頭 nodes インデックス）。
	treeTop int

	// resolveSeq は resolveCmd の発行カウンタ、resolveApplied は適用済みの最大世代。
	// ロック競合で遅延した古い解決が新しい解決を追い越さないよう、発行順の世代で
	// 古い結果を捨てる（B2）。
	resolveSeq     uint64
	resolveApplied uint64

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

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.resolveCmd(), m.treesCmd(), resolveTick(), treesTick())
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
		// ヘルプ行の幅超過時の省略（…）を端末幅に合わせる。
		m.help.Width = m.width
		m.ready = true
		return m, nil

	case resolveTickMsg:
		return m, tea.Batch(m.resolveCmd(), resolveTick())

	case treesTickMsg:
		return m, tea.Batch(m.treesCmd(), treesTick())

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

	return m.updateTreeKey(msg)
}

// updateTreeKey は左ペイン（ツリー）にフォーカスがあるときのキー操作。
func (m *model) updateTreeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case kb.Matches(msg, m.keys.Down):
		return m, m.moveSel(1)
	case kb.Matches(msg, m.keys.Up):
		return m, m.moveSel(-1)
	case kb.Matches(msg, m.keys.Refresh):
		return m, m.treesCmd()
	}
	return m, nil
}

// moveSel はツリーのカーソルを delta 分動かす。
func (m *model) moveSel(delta int) tea.Cmd {
	if len(m.nodes) == 0 {
		return nil
	}
	m.sel = clamp(m.sel+delta, 0, len(m.nodes)-1)
	return nil
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

// ensureSelection はツリー再構築後の選択状態を整えるフック。カーソル位置（sel）は
// buildNodes が確定させるためこの段では追加の処理は無いが、treesMsg のハンドラ構造を
// 保つためコマンドを返す余地を残している。
func (m *model) ensureSelection() tea.Cmd {
	return nil
}

// applyResolved は再解決の結果（設定・状態）をモデルへ写す。selKey 照合はカーソル移動
// しか弾けないため、発行から到着までに遅延した古い解決を発行順の世代（seq）で捨てる。
func (m *model) applyResolved(msg resolvedMsg) {
	// 等号は許容（同一 seq は来ない設計だが安全側に倒す）。
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
