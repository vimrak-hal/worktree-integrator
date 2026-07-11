package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vimrak-hal/worktree-integrator/internal/app"
	"github.com/vimrak-hal/worktree-integrator/internal/app/server"
	"github.com/vimrak-hal/worktree-integrator/internal/app/tree"
	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
	coreserver "github.com/vimrak-hal/worktree-integrator/internal/core/server"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/statedir"
)

// newTestModel は端末サイズ設定済みの（コマンドを一切走らせていない）モデルを返す。
// 統合的な I/O（resolve・switch）はワークフロー側のテストが担っており、ここでは
// モデルの状態遷移だけを検証する。
func newTestModel(t *testing.T) *model {
	t.Helper()
	m := newModel(t.Context(), &config.File{}, statedir.Root{}, &forwarder{})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	return m
}

// serverCfg は api リポジトリに backend / web の 2 サーバーを持つ設定を返す。Spec は
// 空でよい（buildNodes はサーバー名の集合しか見ず、Validate は呼ばない）。
func serverCfg() *config.File {
	return &config.File{Repos: map[string]config.RepoConfig{
		"api": {Servers: map[string]coreserver.Spec{"backend": {}, "web": {}}},
	}}
}

func treesResult(rows ...tree.WorktreeRow) *tree.ListResult {
	return &tree.ListResult{Worktrees: rows}
}

func key(msg string) tea.KeyMsg {
	if len(msg) == 1 {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(msg)}
	}
	switch msg {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEscape}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	}
	panic("unknown key " + msg)
}

// buildNodes は worktree ノードの配下に、メンバー repo に設定された全サーバーを
// repo → server の順で並べ、稼働中サーバーにはそのフィールドを立てる。
func TestBuildNodesLayout(t *testing.T) {
	m := newTestModel(t)
	m.cfg = serverCfg()
	// このテストは「展開中の worktree 配下にサーバーが repo→server 順で並ぶ」レイアウトを
	// 検証する。既定ルールでは全停止の feat-b は折りたたまれサーバーノードが出ないため、
	// 両 worktree を明示展開して従来のレイアウトを固定する（折りたたみ自体は別テスト）。
	m.collapsed = map[string]bool{"feat-a": false, "feat-b": false}
	m.trees = treesResult(
		tree.WorktreeRow{Name: "feat-a", Repos: []tree.RepoCell{{Repo: "api"}},
			Servers: []tree.ServerCell{{Repo: "api", Server: "backend", Pid: 4242}}},
		tree.WorktreeRow{Name: "feat-b", Repos: []tree.RepoCell{{Repo: "api"}}},
	)
	m.buildNodes()

	want := []struct {
		key     string
		running bool
	}{
		{"feat-a", false},
		{"feat-a\x00api/backend", true},
		{"feat-a\x00api/web", false},
		{"feat-b", false},
		{"feat-b\x00api/backend", false},
		{"feat-b\x00api/web", false},
	}
	if len(m.nodes) != len(want) {
		t.Fatalf("nodes = %d, want %d", len(m.nodes), len(want))
	}
	for i, w := range want {
		if m.nodes[i].key() != w.key {
			t.Fatalf("node[%d].key = %q, want %q", i, m.nodes[i].key(), w.key)
		}
		if m.nodes[i].running != w.running {
			t.Fatalf("node[%d].running = %v, want %v", i, m.nodes[i].running, w.running)
		}
	}
	if got := m.nodes[1]; got.pid != 4242 {
		t.Fatalf("running server pid = %d, want 4242", got.pid)
	}
}

// 既定ルール: 稼働中またはクラッシュのサーバーが 1 つでもあれば展開、全停止なら折りたたむ。
// 折りたたみ側はサーバーノードを生成せず（見出しのみ）、ノード構成が変わる。
func TestBuildNodesDefaultCollapse(t *testing.T) {
	m := newTestModel(t)
	m.cfg = serverCfg()
	m.trees = treesResult(
		// feat-a は backend 稼働 → 既定で展開。
		tree.WorktreeRow{Name: "feat-a", Repos: []tree.RepoCell{{Repo: "api"}},
			Servers: []tree.ServerCell{{Repo: "api", Server: "backend", Pid: 4242}}},
		// feat-b は全停止 → 既定で折りたたみ。
		tree.WorktreeRow{Name: "feat-b", Repos: []tree.RepoCell{{Repo: "api"}}},
	)
	m.buildNodes()

	// feat-a: 見出し + backend + web、feat-b: 見出しのみ。
	want := []string{"feat-a", "feat-a\x00api/backend", "feat-a\x00api/web", "feat-b"}
	if len(m.nodes) != len(want) {
		t.Fatalf("nodes = %d, want %d (%+v)", len(m.nodes), len(want), m.nodes)
	}
	for i, w := range want {
		if got := m.nodes[i].key(); got != w {
			t.Fatalf("node[%d].key = %q, want %q", i, got, w)
		}
	}
	if m.nodes[0].collapsed {
		t.Error("稼働中サーバーを持つ feat-a は展開されるべき")
	}
	if !m.nodes[3].collapsed {
		t.Error("全停止の feat-b は折りたたまれるべき")
	}
	// 折りたたみ非依存に配下の全数を数える（集約表示用）。feat-b は停止 2 件のみ。
	if b := m.nodes[3]; b.nStopped != 2 || b.nRunning != 0 || b.nCrashed != 0 {
		t.Fatalf("feat-b aggregate = run%d crash%d stop%d", b.nRunning, b.nCrashed, b.nStopped)
	}
}

// 明示指定（両方向）は既定ルールに優先する。全停止で既定折りたたみの worktree でも
// Space 一発で展開でき、もう一度で明示折りたたみに戻る。
func TestToggleCollapseExplicitOverridesDefault(t *testing.T) {
	m := newTestModel(t)
	m.cfg = serverCfg()
	m.trees = treesResult(tree.WorktreeRow{Name: "feat-b", Repos: []tree.RepoCell{{Repo: "api"}}})
	m.buildNodes()
	// 既定では全停止で折りたたみ、カーソルは見出し上。
	if !m.nodes[m.sel].isWorktree() || !m.nodes[0].collapsed {
		t.Fatalf("既定では feat-b は折りたたみ見出しのはず: %+v", m.nodes)
	}

	m.Update(key(" ")) // Space: 明示展開（既定に勝つ）
	if m.collapsed["feat-b"] {
		t.Fatal("Space は展開の明示指定にするべき")
	}
	if len(m.nodes) != 3 { // 見出し + backend + web
		t.Fatalf("明示展開でサーバーノードが出るべき: nodes=%d", len(m.nodes))
	}

	m.Update(key(" ")) // Space: 明示折りたたみへ戻す
	if !m.collapsed["feat-b"] {
		t.Fatal("再度の Space は折りたたみの明示指定にするべき")
	}
	if len(m.nodes) != 1 {
		t.Fatalf("折りたたみで見出しのみになるべき: nodes=%d", len(m.nodes))
	}
}

// サーバーノード上から折りたたむと、消えるサーバーノードにカーソルが取り残されないよう
// 親 worktree の見出しへカーソルが移る。
func TestToggleCollapseFromServerMovesCursorToHeading(t *testing.T) {
	m := newTestModel(t)
	m.cfg = serverCfg()
	// backend 稼働 → 既定で展開しサーバーノードが並ぶ。
	m.trees = treesResult(
		tree.WorktreeRow{Name: "feat-a", Repos: []tree.RepoCell{{Repo: "api"}},
			Servers: []tree.ServerCell{{Repo: "api", Server: "backend", Pid: 4242}}},
	)
	m.buildNodes()
	m.sel = 1 // backend サーバーノード
	if m.nodes[m.sel].isWorktree() {
		t.Fatal("test setup: サーバーノード上にいるべき")
	}

	m.Update(key(" "))
	if !m.collapsed["feat-a"] {
		t.Fatal("Space は折りたたみの明示指定にするべき")
	}
	if !m.nodes[m.sel].isWorktree() || m.nodes[m.sel].wt != "feat-a" {
		t.Fatalf("カーソルは feat-a 見出しへ移るべき: sel=%d node=%+v", m.sel, m.nodes[m.sel])
	}
}

// J/K は間のサーバーノードを飛ばして次／前の worktree 見出しへ移動し、端では動かない。
func TestJumpWorktree(t *testing.T) {
	m := newTestModel(t)
	m.cfg = serverCfg()
	// 見出しの間にサーバーノードを挟むため両 worktree を明示展開する。
	m.collapsed = map[string]bool{"feat-a": false, "feat-b": false}
	m.trees = treesResult(
		tree.WorktreeRow{Name: "feat-a", Repos: []tree.RepoCell{{Repo: "api"}}},
		tree.WorktreeRow{Name: "feat-b", Repos: []tree.RepoCell{{Repo: "api"}}},
	)
	m.buildNodes()
	// nodes: 0 feat-a, 1 backend, 2 web, 3 feat-b, 4 backend, 5 web
	m.sel = 0

	m.Update(key("J")) // 次の見出し feat-b（間の 1,2 を飛ばす）
	if m.sel != 3 || !m.nodes[m.sel].isWorktree() {
		t.Fatalf("J は feat-b 見出し(3)へ移るべき: sel=%d", m.sel)
	}
	m.Update(key("J")) // 末尾の worktree ではラップせず動かない
	if m.sel != 3 {
		t.Fatalf("末尾の worktree で J は動かないべき: sel=%d", m.sel)
	}
	m.Update(key("K")) // 前の見出し feat-a
	if m.sel != 0 || !m.nodes[m.sel].isWorktree() {
		t.Fatalf("K は feat-a 見出し(0)へ移るべき: sel=%d", m.sel)
	}
	m.Update(key("K")) // 先頭ではラップせず動かない
	if m.sel != 0 {
		t.Fatalf("先頭で K は動かないべき: sel=%d", m.sel)
	}
}

// 不変条件: 折りたたんでも curKey（直近のログ対象）とそのバッファ・tailer は維持される。
// 掃除・有効性判定は折りたたみ非依存の全体集合（allServerKeys）で行うため。
func TestCollapsePreservesBuffersAndCurKey(t *testing.T) {
	m := newTestModel(t)
	m.cfg = serverCfg()
	m.trees = treesResult(
		tree.WorktreeRow{Name: "feat-a", Repos: []tree.RepoCell{{Repo: "api"}},
			Servers: []tree.ServerCell{{Repo: "api", Server: "backend", Pid: 4242}}},
	)
	m.buildNodes()
	k := "feat-a\x00api/backend"
	m.curKey = k
	m.tails[k] = newTailer("/does-not-matter")
	m.bufs[k] = newRing(targetRingCap)

	m.sel = 0 // 見出し上で折りたたむ
	m.Update(key(" "))
	if !m.collapsed["feat-a"] {
		t.Fatal("折りたたみの明示指定になるべき")
	}
	// サーバーノードは m.nodes から消えても、対象・バッファ・tailer は残る。
	if m.curKey != k {
		t.Fatalf("折りたたみで curKey が消えた: %q", m.curKey)
	}
	if m.tails[k] == nil || m.bufs[k] == nil {
		t.Fatal("折りたたみで tailer / バッファが消えてはいけない")
	}
	// ensureSelection も全体集合で判定するので curKey は維持される。
	if cmd := m.ensureSelection(); cmd != nil {
		t.Fatal("生きている curKey に対して再選択コマンドは不要")
	}
	if m.curKey != k || m.tails[k] == nil || m.bufs[k] == nil {
		t.Fatalf("ensureSelection が折りたたみ中の対象を消した: curKey=%q", m.curKey)
	}
}

// サーバーノードへ移動すると表示ログ対象（curKey）が変わり、worktree ノード上では
// 対象が維持される。
func TestMoveSelUpdatesTarget(t *testing.T) {
	m := newTestModel(t)
	m.cfg = serverCfg()
	// カーソル移動でログ対象が切り替わる挙動を見るため、全停止でも両 worktree を明示展開して
	// サーバーノードを出す（既定ルールでは折りたたまれてしまう）。
	m.collapsed = map[string]bool{"feat-a": false, "feat-b": false}
	m.trees = treesResult(
		tree.WorktreeRow{Name: "feat-a", Repos: []tree.RepoCell{{Repo: "api"}}},
		tree.WorktreeRow{Name: "feat-b", Repos: []tree.RepoCell{{Repo: "api"}}},
	)
	m.buildNodes()

	m.moveSel(1) // feat-a/api/backend
	if m.curKey != "feat-a\x00api/backend" {
		t.Fatalf("curKey after first move = %q", m.curKey)
	}
	m.moveSel(1) // feat-a/api/web
	if m.curKey != "feat-a\x00api/web" {
		t.Fatalf("curKey after second move = %q", m.curKey)
	}
	held := m.curKey
	m.moveSel(1) // feat-b (worktree ノード)
	if !m.nodes[m.sel].isWorktree() {
		t.Fatalf("expected cursor on a worktree node, got %+v", m.nodes[m.sel])
	}
	if m.curKey != held {
		t.Fatalf("worktree ノード上で curKey が変わった: %q → %q", held, m.curKey)
	}
}

// パスが変わった対象（外部の switch・--prev トグル）はバッファごと読み直しになる。
func TestApplyResolvedResetsOnPathChange(t *testing.T) {
	m := newTestModel(t)
	dir := t.TempDir()
	oldLog := filepath.Join(dir, "feat-a.log")
	newLog := filepath.Join(dir, "feat-b.log")
	os.WriteFile(oldLog, []byte("old line\n"), 0o644)
	os.WriteFile(newLog, []byte("new line\n"), 0o644)

	k := "feat-a\x00api/backend"
	m.curKey = k
	m.applyResolved(resolvedMsg{selKey: k, path: oldLog, status: &server.StatusResult{}})
	if got := m.bufs[k].slice(); len(got) != 1 || got[0] != "old line" {
		t.Fatalf("buffer = %+v", got)
	}

	// 外部（MCP など）で switch が起きるとログパスが変わる。バッファは新ログの
	// 内容だけになる。
	m.applyResolved(resolvedMsg{selKey: k, path: newLog, status: &server.StatusResult{}})
	if got := m.bufs[k].slice(); len(got) != 1 || got[0] != "new line" {
		t.Fatalf("buffer after path change = %+v", got)
	}
}

func TestFilterNarrowsRenderedLines(t *testing.T) {
	m := newTestModel(t)
	k := "feat-a\x00api/backend"
	m.curKey = k
	log := filepath.Join(t.TempDir(), "a.log")
	os.WriteFile(log, []byte("GET /healthz 200\nERROR boom\nGET /users 200\n"), 0o644)
	m.applyResolved(resolvedMsg{selKey: k, path: log, status: &server.StatusResult{}})

	m.filter = "error"
	m.rebuildLog()
	if view := m.vp.View(); !strings.Contains(view, "boom") || strings.Contains(view, "healthz") {
		t.Fatalf("filtered view = %q", view)
	}
}

// 上方向のスクロールは追従を解除し、f で再開する（ログペインにフォーカス時）。
func TestScrollDisablesFollow(t *testing.T) {
	m := newTestModel(t)
	m.focus = focusLog
	if !m.follow {
		t.Fatal("follow must start enabled")
	}
	m.Update(key("k"))
	if m.follow {
		t.Fatal("scrolling up must disable follow")
	}
	m.Update(key("f"))
	if !m.follow {
		t.Fatal("'f' must re-enable follow")
	}
}

// 操作の実行中は q が完了待ちになり、完了メッセージで終了する。
func TestQuitWaitsForRunningOperation(t *testing.T) {
	m := newTestModel(t)
	m.opRunning = true
	if _, cmd := m.Update(key("q")); cmd != nil {
		t.Fatal("q during an operation must not quit immediately")
	}
	if !m.quitAfterOp {
		t.Fatal("q during an operation must arm quitAfterOp")
	}
	_, cmd := m.Update(opDoneMsg{summary: "done"})
	if cmd == nil {
		t.Fatal("operation completion must quit when quitAfterOp is armed")
	}
	if msg := cmd(); msg != tea.Quit() {
		t.Fatalf("expected tea.Quit, got %#v", msg)
	}
}

// n → reposMsg 受信で作成フォーム（名前入力 + リポジトリ複数選択）が 1 枚で開く。
func TestCreateFlowOpensForm(t *testing.T) {
	m := newTestModel(t)
	m.Update(key("n")) // reposCmd を発行（この時点ではまだフォームは無い）
	if m.form != nil {
		t.Fatal("n before reposMsg must not open a form yet")
	}

	m.Update(reposMsg{res: &app.ReposResult{Repos: []app.RepoInfo{{Name: "api"}, {Name: "web"}}}})
	if m.form == nil || m.formKind != formCreate {
		t.Fatalf("reposMsg must open the create form, got form=%v kind=%d", m.form != nil, m.formKind)
	}
	// 名前入力欄とリポジトリ選択の両方が 1 枚のフォームに含まれる。
	view := m.form.View()
	for _, want := range []string{"worktree 名", "作成先リポジトリ", "api", "web"} {
		if !strings.Contains(view, want) {
			t.Errorf("create form view missing %q", want)
		}
	}
}

// 作成フォームの完了処理: 名前とリポジトリを持つ完了状態から createCmd を dispatch する。
func TestFinishCreateForm(t *testing.T) {
	m := newTestModel(t)
	m.formKind = formCreate
	m.formName = "feat-x"
	m.formRepos = []string{"api"}
	_, cmd := m.finishForm()
	if cmd == nil {
		t.Fatal("finishForm with a name and repos must return a create command")
	}
	if !m.opRunning {
		t.Fatal("create must mark an operation as running")
	}
}

// 名前が空・リポジトリ 0 件の作成フォームは note を出して破棄する（操作は起きない）。
func TestFinishCreateFormRejectsEmpty(t *testing.T) {
	m := newTestModel(t)
	m.formKind = formCreate
	m.formName = "  "
	m.formRepos = []string{"api"}
	if _, cmd := m.finishForm(); cmd != nil || m.opRunning {
		t.Fatal("empty name must not start an operation")
	}
	if !m.noteErr {
		t.Fatal("empty name must set an error note")
	}

	m2 := newTestModel(t)
	m2.formKind = formCreate
	m2.formName = "feat-x"
	m2.formRepos = nil
	if _, cmd := m2.finishForm(); cmd != nil || m2.opRunning {
		t.Fatal("zero repos must not start an operation")
	}
}

// D は削除確認フォームを開き、対象を保持する。formConfirm=true の完了で removeCmd。
func TestRemoveConfirmFlow(t *testing.T) {
	m := newTestModel(t)
	m.trees = treesResult(tree.WorktreeRow{Name: "feat-a"})
	m.buildNodes()
	m.Update(key("D"))
	if m.form == nil || m.formKind != formRemove || m.promptTarget != "feat-a" {
		t.Fatalf("D must open the remove form for feat-a, got form=%v kind=%d target=%q",
			m.form != nil, m.formKind, m.promptTarget)
	}

	// 承認（true）で削除コマンドが発行される。
	m.formConfirm = true
	_, cmd := m.finishForm()
	if cmd == nil {
		t.Fatal("confirmed remove must return a remove command")
	}
	if !m.opRunning {
		t.Fatal("remove must mark an operation as running")
	}
}

// 削除確認を否定（false）した完了では何も起きない。
func TestRemoveConfirmDeclined(t *testing.T) {
	m := newTestModel(t)
	m.promptTarget = "feat-a"
	m.formKind = formRemove
	m.formConfirm = false
	if _, cmd := m.finishForm(); cmd != nil || m.opRunning {
		t.Fatal("declined remove must not start an operation")
	}
}

// a は選択 worktree の現在の別名を別名フォームにプリフィルする。
func TestAliasPrefill(t *testing.T) {
	m := newTestModel(t)
	m.trees = treesResult(tree.WorktreeRow{Name: "feat-a", Alias: "ログイン画面"})
	m.buildNodes()
	m.Update(key("a"))
	if m.form == nil || m.formKind != formAlias {
		t.Fatalf("a must open the alias form, got form=%v kind=%d", m.form != nil, m.formKind)
	}
	if m.formAlias != "ログイン画面" {
		t.Fatalf("alias form must prefill the current alias, got %q", m.formAlias)
	}
	if !strings.Contains(m.form.View(), "ログイン画面") {
		t.Error("alias form view must show the prefilled alias")
	}
}

// Esc 相当（StateAborted）でフォームが畳まれ、note は出ない。
func TestFormAbortClearsWithoutNote(t *testing.T) {
	m := newTestModel(t)
	m.trees = treesResult(tree.WorktreeRow{Name: "feat-a"})
	m.buildNodes()
	m.Update(key("D"))
	if m.form == nil {
		t.Fatal("D must open the remove form")
	}
	m.Update(key("esc"))
	if m.form != nil || m.formKind != formNone {
		t.Fatalf("Esc must clear the form, got form=%v kind=%d", m.form != nil, m.formKind)
	}
	if m.note != "" {
		t.Fatalf("aborting a form must not leave a note, got %q", m.note)
	}
}

// C1: 作成フォーム最前面での Esc はフォームを畳む（updateKey が横取り。フィルタ非入力中）。
func TestFormEscClosesCreateForm(t *testing.T) {
	m := newTestModel(t)
	m.Update(key("n"))
	m.Update(reposMsg{res: &app.ReposResult{Repos: []app.RepoInfo{{Name: "api"}}}})
	if m.form == nil || m.formKind != formCreate {
		t.Fatalf("create form must be open, got form=%v kind=%d", m.form != nil, m.formKind)
	}
	m.Update(key("esc"))
	if m.form != nil || m.formKind != formNone {
		t.Fatalf("Esc must close the create form, got form=%v kind=%d", m.form != nil, m.formKind)
	}
}

// C1: 削除確認フォームでの Esc はフォームを畳み、removeCmd を起動しない。
func TestFormEscDoesNotRunRemove(t *testing.T) {
	m := newTestModel(t)
	m.trees = treesResult(tree.WorktreeRow{Name: "feat-a"})
	m.buildNodes()
	m.Update(key("D"))
	if m.form == nil || m.formKind != formRemove {
		t.Fatalf("D must open the remove form, got form=%v kind=%d", m.form != nil, m.formKind)
	}
	_, cmd := m.Update(key("esc"))
	if m.form != nil || m.formKind != formNone {
		t.Fatalf("Esc must close the remove form, got form=%v kind=%d", m.form != nil, m.formKind)
	}
	if m.opRunning {
		t.Fatal("Esc must not start a remove operation")
	}
	if cmd != nil {
		t.Fatal("Esc on the remove form must not dispatch a command")
	}
}

// C1: formFiltering は Input にフォーカスがあるフォーム（フィルタ非対応）では false。
func TestFormFilteringFalseForInputFocus(t *testing.T) {
	name := ""
	var sel []string
	form := newCreateForm([]app.RepoInfo{{Name: "api"}}, &name, &sel, 40)
	if formFiltering(form) {
		t.Fatal("Input フォーカスのフォームをフィルタ入力中と判定してはいけない")
	}
	if formFiltering(nil) {
		t.Fatal("nil フォームは常に false")
	}
}

// B1: reposMsg 到着前に別フォーム（削除）を開くと、遅れて届いた候補は破棄され、開いて
// いるフォームを作成フォームで潰さない。
func TestReposMsgGuardedWhileFormOpen(t *testing.T) {
	m := newTestModel(t)
	m.trees = treesResult(tree.WorktreeRow{Name: "feat-a"})
	m.buildNodes()
	m.Update(key("n")) // reposCmd 発行（フォームはまだ無い）
	m.Update(key("D")) // 先に削除フォームを開く
	if m.formKind != formRemove {
		t.Fatalf("D must open the remove form, got kind=%d", m.formKind)
	}
	// 遅れて届いた候補。削除フォームを潰さず破棄される。
	m.Update(reposMsg{res: &app.ReposResult{Repos: []app.RepoInfo{{Name: "api"}}}})
	if m.formKind != formRemove {
		t.Fatalf("late reposMsg must not replace the open form, got kind=%d", m.formKind)
	}
}

// B1（非回帰）: 通常の n → reposMsg で作成フォームが開く。
func TestReposMsgOpensFormWhenIdle(t *testing.T) {
	m := newTestModel(t)
	m.Update(key("n"))
	m.Update(reposMsg{res: &app.ReposResult{Repos: []app.RepoInfo{{Name: "api"}}}})
	if m.form == nil || m.formKind != formCreate {
		t.Fatalf("idle reposMsg must open the create form, got form=%v kind=%d", m.form != nil, m.formKind)
	}
}

// C10: 操作実行中の n は候補取得を発行せず note を出す（入力後に弾かれる無駄を防ぐ）。
func TestNewGuardedWhileOpRunning(t *testing.T) {
	m := newTestModel(t)
	m.trees = treesResult(tree.WorktreeRow{Name: "feat-a"})
	m.buildNodes()
	m.opRunning = true
	if _, cmd := m.Update(key("n")); cmd != nil {
		t.Fatal("n during an operation must not dispatch reposCmd")
	}
	if !m.noteErr {
		t.Fatal("n during an operation must set an error note")
	}
}

// B2: 新しい seq の path 適用後、古い seq の missing=true が届いてもバッファは消えない
// （発行順の世代で古い解決を弾く）。
func TestApplyResolvedIgnoresStaleSeq(t *testing.T) {
	m := newTestModel(t)
	log := filepath.Join(t.TempDir(), "a.log")
	os.WriteFile(log, []byte("live line\n"), 0o644)
	k := "feat-a\x00api/backend"
	m.curKey = k

	// seq=2 で path を適用（バッファ生成）。
	m.applyResolved(resolvedMsg{seq: 2, selKey: k, path: log, status: &server.StatusResult{}})
	if m.bufs[k] == nil || m.tails[k] == nil {
		t.Fatal("path resolution must create a tailer and a buffer")
	}

	// 遅れて届いた古い seq=1 の missing。無視されバッファ・tailer は残る。
	m.applyResolved(resolvedMsg{seq: 1, selKey: k, missing: true, status: &server.StatusResult{}})
	if m.bufs[k] == nil || m.tails[k] == nil {
		t.Fatal("stale resolution must not delete tails/bufs")
	}
	if m.curMissing {
		t.Fatal("stale missing must not mark the target missing")
	}
}

// B3: ノードが消えると buildNodes が該当 key の tailer / バッファを掃除する
// （worktree の作成→削除の繰り返しでリングが残り続けるのを防ぐ）。
func TestBuildNodesEvictsGoneTargets(t *testing.T) {
	m := newTestModel(t)
	m.cfg = serverCfg()
	gone := "feat-a\x00api/backend"
	m.tails[gone] = newTailer("/does-not-matter")
	m.bufs[gone] = newRing(targetRingCap)
	m.curKey = "" // 消える key を保護しない

	// feat-a を含まない一覧で再構築 → gone のノードは存在しない。
	m.trees = treesResult(tree.WorktreeRow{Name: "feat-b", Repos: []tree.RepoCell{{Repo: "api"}}})
	m.buildNodes()

	if _, ok := m.tails[gone]; ok {
		t.Fatal("gone target tailer must be evicted")
	}
	if _, ok := m.bufs[gone]; ok {
		t.Fatal("gone target buffer must be evicted")
	}
}

// C5: follow オフで YOffset を進めた状態のリサイズは YOffset を保つ（作り直すと 0 に戻る）。
func TestWindowResizePreservesYOffset(t *testing.T) {
	m := newTestModel(t)
	k := "feat-a\x00api/backend"
	m.curKey = k
	log := filepath.Join(t.TempDir(), "a.log")
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString("line\n")
	}
	os.WriteFile(log, []byte(b.String()), 0o644)
	m.applyResolved(resolvedMsg{selKey: k, path: log, status: &server.StatusResult{}})

	m.focus = focusLog
	m.follow = false
	m.vp.SetYOffset(50)
	off := m.vp.YOffset
	if off == 0 {
		t.Fatal("test setup: YOffset must be advanced")
	}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	if m.vp.YOffset != off {
		t.Fatalf("resize must preserve YOffset, got %d want %d", m.vp.YOffset, off)
	}
}

// C6: doctor 遷移では直前のログ閲覧の YOffset を捨て、必ず先頭から表示する。
func TestDoctorTransitionResetsYOffset(t *testing.T) {
	m := newTestModel(t)
	m.vp.SetContent(strings.Repeat("x\n", 100))
	m.vp.SetYOffset(40)
	if m.vp.YOffset == 0 {
		t.Fatal("test setup: YOffset must be advanced")
	}
	m.Update(opDoneMsg{summary: "doctor 完了", doctorText: []string{"a", "b", "c"}})
	if !m.doctorMode {
		t.Fatal("doctorText must switch to doctor mode")
	}
	if m.vp.YOffset != 0 {
		t.Fatalf("doctor transition must go to top, got YOffset=%d", m.vp.YOffset)
	}
}
