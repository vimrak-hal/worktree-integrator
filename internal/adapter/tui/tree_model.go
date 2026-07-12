package tui

import (
	"sort"
	"strings"

	"github.com/vimrak-hal/worktree-integrator/internal/app/server"
	"github.com/vimrak-hal/worktree-integrator/internal/app/tree"
	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
	coreserver "github.com/vimrak-hal/worktree-integrator/internal/core/server"
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

// treeModel は左ペイン（WORKTREES ツリー）の状態。worktree → サーバーのノード列・選択
// カーソル・折りたたみ指定を持つ。ログ対象（curKey）はここには持たず、ツリーとログの
// 結合（カーソル移動での対象切替・掃除）はルート model の調停メソッドが担う。
type treeModel struct {
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
	// 全体集合（折りたたみ非依存）。折りたたみで nodes からサーバーノードが消えても、
	// ログ（tails/bufs）の掃除・ensureSelection の curKey 有効性判定はこの集合で行う。
	allServerKeys map[string]bool
}

// buildNodes は trees・設定のサーバー定義・status から nodes を再構築する。各 worktree
// ノードの配下に、その worktree のメンバー repo に設定されたサーバー（∪ 稼働中に
// 現れる (repo, server)）を repo 名 → server 名の順で並べる。カーソル位置は可能なら
// 同じノードの key を保つ。ログ側の掃除（tails/bufs）はここでは行わず、ルートの調停
// メソッドが allServerKeys を使って別途行う（折りたたみでバッファが消えない不変条件）。
func (tm *treeModel) buildNodes(cfg *config.File) {
	prevKey := ""
	if tm.sel >= 0 && tm.sel < len(tm.nodes) {
		prevKey = tm.nodes[tm.sel].key()
	}

	var nodes []node
	// allKeys は「worktree × 設定上のサーバー定義（∪ 稼働中に現れる座標）」の全体集合。
	// 折りたたみに依存せず（折りたたんだ worktree のサーバーもここに数える）、ログ側の
	// 掃除と ensureSelection の curKey 有効性判定はこの集合で行う。折りたたみでバッファや
	// ログ対象が消えてはいけない、という不変条件のための土台。
	allKeys := map[string]bool{}
	// liveWts は現存する worktree 名の集合。明示指定マップ（collapsed）の掃除に使う。
	liveWts := map[string]bool{}
	if tm.trees != nil {
		// crashed 判定用: サーバーノードの key → クラッシュ。
		crashed := map[string]bool{}
		if tm.status != nil {
			for _, r := range tm.status.Rows {
				if r.State == server.StateCrashed && r.Worktree != "" {
					crashed[r.Worktree+"\x00"+r.Repo+"/"+r.Server] = true
				}
			}
		}
		// 設定上のサーバー定義（読めない設定はサーバーなし扱い）。
		var servers coreserver.Config
		if cfg != nil {
			servers = cfg.ServersConfig()
		}

		for _, wt := range tm.trees.Worktrees {
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
			collapsed := tm.isCollapsed(wt.Name, nRunning > 0 || nCrashed > 0)
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
	tm.nodes = nodes
	tm.allServerKeys = allKeys

	// 旧カーソルのノードが残っていれば追従、消えていれば clamp。
	tm.sel = 0
	for i, n := range nodes {
		if n.key() == prevKey {
			tm.sel = i
			break
		}
	}
	tm.sel = clamp(tm.sel, 0, max(0, len(nodes)-1))

	// 明示的な折りたたみ指定は、worktree が消えたらここで掃除する。ログ（tails/bufs）の
	// 掃除は allServerKeys を使ってルートの調停メソッドが行う。
	for wt := range tm.collapsed {
		if !liveWts[wt] {
			delete(tm.collapsed, wt)
		}
	}
}

// moveSel はカーソルを delta 分動かし、サーバーノードに乗ったらその key を返す
// （worktree ノード・ノード無しは onServer=false）。乗った対象のログ切り替えは
// 呼び出し側（ルートの調停）が行う。
func (tm *treeModel) moveSel(delta int) (key string, onServer bool) {
	if len(tm.nodes) == 0 {
		return "", false
	}
	tm.sel = clamp(tm.sel+delta, 0, len(tm.nodes)-1)
	n := tm.nodes[tm.sel]
	if n.isWorktree() {
		return "", false
	}
	return n.key(), true
}

// isCollapsed は worktree の折りたたみ状態を決める。不変条件: ユーザーの明示指定
// （collapsed にキーが存在する）は両方向とも既定ルールに優先する。指定が無ければ
// 既定ルール（anyActive=稼働中またはクラッシュが 1 つでもある → 展開、全停止 → 折りたたみ）。
func (tm *treeModel) isCollapsed(wt string, anyActive bool) bool {
	if v, ok := tm.collapsed[wt]; ok {
		return v
	}
	return !anyActive
}

// toggleCollapse はカーソル位置の worktree（サーバーノード上ならその親 worktree）の
// 折りたたみをトグルし、明示指定として collapsed に記録する。現在の実効状態は直近の
// buildNodes が見出しノードへ書いた collapsed を正として反転するため、既定ルールで
// 折りたたまれている worktree でも Space 一発で展開できる（明示展開が既定ルールに勝つ）。
// サーバーノード上から折りたたんだ場合は、消えるサーバーノードにカーソルが取り残されない
// よう見出しへ移す。
func (tm *treeModel) toggleCollapse(cfg *config.File) {
	wt, ok := tm.selectedWorktree()
	if !ok {
		return
	}
	tm.setCollapsed(cfg, !tm.worktreeCollapsed(wt))
}

// setCollapsed はカーソル位置の worktree（サーバーノード上ならその親 worktree）の折りたたみを
// want に明示指定する（h=折りたたむ→true、l=展開→false）。既に want の状態なら何もしない。
// toggleCollapse と同じく、サーバーノード上から折りたたむとカーソルを見出しへ移す。
func (tm *treeModel) setCollapsed(cfg *config.File, want bool) {
	wt, ok := tm.selectedWorktree()
	if !ok {
		return
	}
	onServer := !tm.nodes[tm.sel].isWorktree()
	tm.collapsed[wt] = want
	tm.buildNodes(cfg)
	if onServer && want {
		tm.selectWorktreeHeading(wt)
	}
}

// worktreeCollapsed は現在の見出しノードが持つ実効的な折りたたみ状態を返す（明示指定と
// 既定ルールを buildNodes が解決済みの値）。見出しが無ければ false。
func (tm *treeModel) worktreeCollapsed(wt string) bool {
	for _, n := range tm.nodes {
		if n.isWorktree() && n.wt == wt {
			return n.collapsed
		}
	}
	return false
}

// selectWorktreeHeading はカーソルを指定 worktree の見出しノードへ移す。
func (tm *treeModel) selectWorktreeHeading(wt string) {
	for i, n := range tm.nodes {
		if n.isWorktree() && n.wt == wt {
			tm.sel = i
			return
		}
	}
}

// jumpWorktree はカーソルを次（dir=1）／前（dir=-1）の worktree 見出しノードへ移す。
// 間のサーバーノードは飛ばし、端ではラップせず（見つからなければ）動かない。
func (tm *treeModel) jumpWorktree(dir int) {
	for i := tm.sel + dir; i >= 0 && i < len(tm.nodes); i += dir {
		if tm.nodes[i].isWorktree() {
			tm.sel = i
			return
		}
	}
}

// selectedWorktree はカーソル位置のノードが属する worktree 名を返す（worktree ノード・
// サーバーノードのどちらでも親の worktree 名になる）。
func (tm *treeModel) selectedWorktree() (string, bool) {
	if tm.sel < 0 || tm.sel >= len(tm.nodes) {
		return "", false
	}
	return tm.nodes[tm.sel].wt, true
}

// selectedAlias はカーソル位置の worktree の現在の別名を返す（alias プロンプトの
// プリフィル用）。
func (tm *treeModel) selectedAlias() string {
	wt, ok := tm.selectedWorktree()
	if !ok {
		return ""
	}
	for _, n := range tm.nodes {
		if n.isWorktree() && n.wt == wt {
			return n.alias
		}
	}
	return ""
}

// firstServerKey は最初のサーバーノードの key を返す（無ければ ok=false）。
func (tm *treeModel) firstServerKey() (string, bool) {
	for _, n := range tm.nodes {
		if !n.isWorktree() {
			return n.key(), true
		}
	}
	return "", false
}
