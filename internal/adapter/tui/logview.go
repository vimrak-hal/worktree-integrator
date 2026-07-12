package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
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
)

// logModel は右ペイン（選択サーバーのログ）の状態。表示中の対象 1 本だけを増分読み
// （tailer）してリング（bufs）へ貯め、ビューポート（vp）へ流す。ツリーとの結合（対象
// curKey の切替・掃除）はルート model の調停メソッドが担い、ここはログ表示に閉じる。
type logModel struct {
	// curKey は表示中サーバーノードの key（空はプレースホルダ）。
	curKey     string
	curPath    string
	curMissing bool
	curReadErr string
	tails      map[string]*tailer
	bufs       map[string]*ring
	vp         viewport.Model
	// ready は初回の WindowSizeMsg で vp が寸法付きで生成済みかどうか。未 ready の
	// 間は rebuild を空振りさせ、寸法 0 のビューポートへ書き込まないようにする。
	ready bool
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
	// prompt はフィルタ入力モード（promptFilter の間はキーを updateFilterKey が消費する）。
	prompt promptMode
	// resolveSeq は resolveCmd の発行カウンタ、resolveApplied は適用済みの最大世代。
	// selKey 照合はカーソル移動しか弾けず、ロック競合で遅延した古い解決が新しい解決を
	// 追い越すと path が巻き戻るため、発行順の世代で古い結果を捨てる（B2）。
	resolveSeq     uint64
	resolveApplied uint64
}

// resize は初回に vp を生成し、以降は幅・高さだけを更新する。作り直すと YOffset が 0 に
// 戻り、追従オフで過去ログを読んでいる最中のリサイズで先頭へ飛ぶため、幅・高さの直接
// 代入で YOffset を保つ（viewport は派生状態を都度計算する）。
func (lm *logModel) resize(w, h int) {
	if !lm.ready {
		lm.vp = viewport.New(w, h)
		lm.ready = true
	} else {
		lm.vp.Width = w
		lm.vp.Height = h
	}
}

// prune は allServerKeys（折りたたみ非依存の全体集合）に無い対象の tailer / バッファを
// 掃除する。表示中の対象（curKey）は防御的に常に残す。ルートの buildNodes 調停から
// buildNodes 後に呼ばれる（折りたたみでバッファが消えない不変条件の要）。
func (lm *logModel) prune(allServerKeys map[string]bool) {
	for k := range lm.tails {
		if !allServerKeys[k] && k != lm.curKey {
			delete(lm.tails, k)
		}
	}
	for k := range lm.bufs {
		if !allServerKeys[k] && k != lm.curKey {
			delete(lm.bufs, k)
		}
	}
}

// prepareTarget は curKey を切り替えた直後の処理。既存バッファがあれば過去分を即座に
// 表示し、無ければパス解決までプレースホルダを出す。再解決の発行はルートの調停
// （selectTarget）が resolveCmd で行う。
func (lm *logModel) prepareTarget() {
	lm.follow = true
	lm.curReadErr = ""
	if _, ok := lm.bufs[lm.curKey]; !ok {
		// まだバッファが無い対象はパス解決までプレースホルダを出す。
		lm.curPath = ""
		lm.curMissing = false
	}
	lm.rebuild()
}

// applyPath は再解決結果のうち選択対象のログパス部分を写す。selKey が現在の curKey と
// 一致するときだけログ対象を更新する（発行から到着までに選択が動いた古い結果を無視する
// ための照合）。パスが変わった対象（外部の switch や --prev トグル）は tailer とバッファを
// 作り直し、新しいログを末尾から読み直す。
func (lm *logModel) applyPath(msg resolvedMsg) {
	if msg.selKey != lm.curKey || lm.curKey == "" {
		return
	}
	switch {
	case msg.missing:
		// ログ未生成: 追跡をやめてプレースホルダを出す（期待パスは保持）。
		lm.curMissing = true
		lm.curPath = msg.path
		delete(lm.tails, lm.curKey)
		delete(lm.bufs, lm.curKey)
	case msg.path != "":
		lm.curMissing = false
		if msg.path != lm.curPath || lm.tails[lm.curKey] == nil {
			lm.curPath = msg.path
			lm.tails[lm.curKey] = newTailer(msg.path)
			lm.bufs[lm.curKey] = newRing(targetRingCap)
			lm.curReadErr = ""
		}
		lm.pollTail()
	}
}

// pollTail は選択中の対象 1 本だけを増分読みし、バッファへ追記する。追記があったかを
// 返す（全対象を毎ティック読むのは廃止 — 表示しているログのみ追う）。
func (lm *logModel) pollTail() bool {
	if lm.curKey == "" {
		return false
	}
	tail := lm.tails[lm.curKey]
	buf := lm.bufs[lm.curKey]
	if tail == nil || buf == nil {
		return false
	}
	lines, err := tail.poll()
	if err != nil {
		lm.curReadErr = err.Error()
		return false
	}
	lm.curReadErr = ""
	if len(lines) == 0 {
		return false
	}
	buf.push(lines...)
	return true
}

// updateFilterKey はフィルタ入力中のキー操作（ライブ反映）。
func (lm *logModel) updateFilterKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.Type {
	case tea.KeyEnter:
		lm.prompt = promptNone
		lm.filtering = false
		lm.input.Blur()
	case tea.KeyEscape:
		lm.prompt = promptNone
		lm.filtering = false
		lm.filter = ""
		lm.input.SetValue("")
		lm.input.Blur()
		lm.rebuild()
	default:
		var cmd tea.Cmd
		lm.input, cmd = lm.input.Update(msg)
		// ライブフィルタ: 1 打鍵ごとに絞り込みが反映される。
		lm.filter = lm.input.Value()
		lm.rebuild()
		return cmd
	}
	return nil
}

// beginFilter はフィルタ入力モードへ入る（/ キー）。現在のフィルタ値をプリフィルする。
func (lm *logModel) beginFilter() {
	lm.prompt = promptFilter
	lm.filtering = true
	lm.input.Prompt = "/"
	lm.input.Placeholder = "フィルタ（部分一致）"
	lm.input.SetValue(lm.filter)
	lm.input.CursorEnd()
	lm.input.Focus()
}

// clearFilter はフィルタを解除して再描画する（フィルタが空なら何もしない）。
func (lm *logModel) clearFilter() {
	if lm.filter != "" {
		lm.filter = ""
		lm.input.SetValue("")
		lm.rebuild()
	}
}

// toggleFollow は末尾追従をトグルし、有効化時は末尾へ飛ぶ。
func (lm *logModel) toggleFollow() {
	lm.follow = !lm.follow
	if lm.follow {
		lm.vp.GotoBottom()
	}
}

// rebuild は現在の選択・フィルタからログビューポート（vp）の内容を組み立て直す。
// doctor 結果は専用ダイアログ（dvp）で描くため、ここでは分岐しない（右ペインは常にログ）。
func (lm *logModel) rebuild() {
	if !lm.ready {
		return
	}
	if lm.curKey == "" {
		lm.vp.SetContent("サーバーノードを選択してください")
		return
	}
	buf := lm.bufs[lm.curKey]
	if buf == nil {
		if lm.curMissing {
			lm.vp.SetContent("ログがまだありません (" + lm.curPath + ")")
		} else {
			lm.vp.SetContent("ログを解決中…")
		}
		return
	}

	filter := strings.ToLower(lm.filter)
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
	if lm.wrap {
		for i, text := range rendered {
			rendered[i] = wrapDisplay(text, lm.vp.Width)
		}
	}
	lm.vp.SetContent(strings.Join(rendered, "\n"))
	if lm.follow {
		lm.vp.GotoBottom()
	}
}

// logTitle は右ペインの見出し（常にグレー）: 対象（repo/server @ worktree）と、モードの
// フラグ（追従・前世代・フィルタ／入力中の input.View()）をピルで添える。フォーム・doctor は
// 別のフローティングダイアログで描くため、この見出しは常にログの見出しになる。
func (lm *logModel) logTitle() string {
	label := "ログ"
	if lm.curKey != "" {
		if wt, repo, srv, ok := splitKey(lm.curKey); ok {
			label = fmt.Sprintf("ログ: %s/%s @ %s", repo, srv, wt)
		}
	}
	title := styPaneTitle.Render(label)
	if pills := lm.logPills(); pills != "" {
		title += " " + pills
	}
	return title
}

// logPills はログ見出しへ添えるフラグのピル（バッジ）列を組む。文字色のみで表現し
// （背景色はテーマ追従のため使わない）、前世代/フィルタ=シアン・読取失敗=赤。
// フィルタ入力中は textinput の生ビューをそのまま見せる。
// 既定状態（末尾追従）にはバッジを出さない — 常時表示はノイズで、注意が要るのは
// 上へスクロールして追従が切れている間だけなので、そのときだけ黄で示す。
func (lm *logModel) logPills() string {
	var pills []string
	if !lm.follow {
		pills = append(pills, styPillWarn.Render("[追従停止]"))
	}
	if lm.prev {
		pills = append(pills, styPillAccent.Render("[前世代]"))
	}
	if lm.prompt == promptFilter {
		pills = append(pills, lm.input.View())
	} else if lm.filter != "" {
		pills = append(pills, styPillAccent.Render("[/"+lm.filter+"]"))
	}
	if lm.curReadErr != "" {
		pills = append(pills, styPillError.Render("[読取失敗]"))
	}
	return strings.Join(pills, " ")
}

// rightLines は右ペインの行（常にログのビューポート）を返す。フォーム・doctor は中央の
// フローティングダイアログで描くため、右ペインは常にログを出す。
func (lm *logModel) rightLines() []string {
	return strings.Split(lm.vp.View(), "\n")
}
