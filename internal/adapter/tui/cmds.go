package tui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vimrak-hal/worktree-integrator/internal/adapter/render"
	"github.com/vimrak-hal/worktree-integrator/internal/app"
	"github.com/vimrak-hal/worktree-integrator/internal/app/action"
	"github.com/vimrak-hal/worktree-integrator/internal/app/server"
	"github.com/vimrak-hal/worktree-integrator/internal/app/tree"
	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
	"github.com/vimrak-hal/worktree-integrator/internal/core/git/worktree"
	coreserver "github.com/vimrak-hal/worktree-integrator/internal/core/server"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/childio"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/statedir"
)

// ポーリング間隔。tail は増分読みだけなので速く、resolve は状態ファイルロックと
// プロセス生存確認（Probe）を伴うため控えめに、trees はファイルスキャンを伴うため
// さらに遅くする。resolve の再解決が MCP・別 CLI による並行変更（switch など）を
// 拾う経路である。
const (
	tailInterval    = 500 * time.Millisecond
	resolveInterval = 2 * time.Second
	treesInterval   = 10 * time.Second
)

type (
	tailTickMsg    time.Time
	resolveTickMsg time.Time
	treesTickMsg   time.Time
)

// resolvedMsg は resolveCmd の結果: 再読み込み済みの設定・全体のサーバー状態、および
// 選択中サーバーノード 1 件のログパス解決。selKey は解決の対象だった key であり、
// これが空なら path / missing は無意味（ステータスのみの更新）。selKey が現在の
// curKey と一致するときだけモデルはログ対象を更新する（発行から到着までの間に選択が
// 動いた古い結果を捨てるための照合）。
type resolvedMsg struct {
	// seq は発行順の世代番号。applyResolved が古い解決の追い越しを弾くために使う
	// （エラー戻りにも載せる）。
	seq     uint64
	cfg     *config.File
	status  *server.StatusResult
	selKey  string
	path    string
	missing bool
	warn    string
	err     error
}

// treesMsg は treesCmd の結果（worktree 一覧）。
type treesMsg struct {
	res *tree.ListResult
	err error
}

// reposMsg は reposCmd の結果（create の作成先リポジトリ候補）。
type reposMsg struct {
	res *app.ReposResult
	err error
}

// eventMsg はイベント履歴の 1 行。サーバーのライフサイクルイベントと worktree 作成の
// 進捗（fetch / 作成 / 型付き Note）の両方がこの 1 経路で流れる。ワークフローは
// Update の外（tea.Cmd の goroutine）で走るため、forwarder がモデルを直接触らず
// p.Send で正規のメッセージ経路に乗せる。
type eventMsg struct{ line string }

// opDoneMsg は統合操作（switch / stop / create / remove / alias / doctor）の完了。
// summary は日本語の 1 行サマリで、err が非 nil でも部分的な成功を含みうる。
// doctorText は doctor 実行時のみ非 nil で、結果ペインへ表示するために描画済みの
// テキストを行分割して運ぶ（モデル側で render を呼ばずに済ませる）。
type opDoneMsg struct {
	summary    string
	err        error
	doctorText []string
}

func tailTick() tea.Cmd {
	return tea.Tick(tailInterval, func(t time.Time) tea.Msg { return tailTickMsg(t) })
}

func resolveTick() tea.Cmd {
	return tea.Tick(resolveInterval, func(t time.Time) tea.Msg { return resolveTickMsg(t) })
}

func treesTick() tea.Cmd {
	return tea.Tick(treesInterval, func(t time.Time) tea.Msg { return treesTickMsg(t) })
}

// forwarder はワークフローのライブイベントを Bubble Tea プログラムへ転送する
// app.Progress の実装。p は Run が tea.NewProgram の直後（ユーザー操作が始まる前）に
// 設定する。作成進捗（Update / Event）とサーバーイベント（ServerEvent）のいずれも
// 短い日本語 1 行に整形して eventMsg で送る。
type forwarder struct{ p *tea.Program }

// Update は worktree 作成の進捗遷移（fetch 中 / 作成中）を転送する。語彙は render に
// 一本化されている（CLI/MCP/TUI で同じラベル・網羅チェックを共有する）。
func (f *forwarder) Update(repo string, state worktree.Progress) {
	if f.p != nil {
		f.p.Send(eventMsg{line: fmt.Sprintf("[%s] %s", repo, render.ProgressLabel(state))})
	}
}

// Event は worktree 作成の型付き途中経過イベントを転送する。
func (f *forwarder) Event(repo string, n worktree.Note) {
	if f.p != nil {
		f.p.Send(eventMsg{line: fmt.Sprintf("[%s] %s", repo, render.NoteLine(n))})
	}
}

// ServerEvent はサーバーのライフサイクルイベントを転送する。本文は render の共有語彙を
// 使い、TUI 向けにはタグのプレフィクスだけを被せる（行頭インデント・改行は付けない）。
func (f *forwarder) ServerEvent(repo, srv string, ev coreserver.Event) {
	if f.p != nil {
		f.p.Send(eventMsg{line: fmt.Sprintf("[%s] %s", repo+"/"+srv, render.ServerEventText(ev))})
	}
}

// buildApp は TUI 配下でワークフローを駆動する App を組み立てる。子プロセス
// （setup / on_switch などのライフサイクルコマンド）の出力は破棄する: TUI が端末を
// 専有しているため、stdout / stderr へ書くと画面が壊れる（MCP の childio.Quiet と
// 同じ理由の TUI 版）。失敗の内容は型付きの Failure（起動失敗ならログ末尾つき）で
// 報告される。
func buildApp(cfg *config.File, root statedir.Root, fw *forwarder) *app.App {
	// quiet は ChildIO と procctl の両方へ同じものが渡り（app.New がペアで導出する）、
	// ライフサイクルコマンドとサーバープロセスのどちらの出力も破棄される。Selector は
	// 既定の nil（非対話）。
	quiet := childio.Streams{Stdin: nil, Stdout: io.Discard, Stderr: io.Discard}
	return app.New(cfg, root, quiet, app.WithProgress(fw))
}

// serverCommand は現在の設定から server 系ワークフローの実行コンテキストを解決する
// （ディレクトリのオーバーライドは TUI に無い — 設定と WT_* 環境変数から解決する）。
func serverCommand(cfg *config.File) (action.ServerCommand, error) {
	return action.NewServerCommand(action.ServerCommandInput{
		File:   cfg,
		Getenv: os.Getenv,
		Home:   os.UserHomeDir,
	})
}

// opStartFailed は統合操作を開始できなかったとき（前段の実行コンテキスト解決・対象名の
// 解析・作成アクションの組み立ての失敗）の完了メッセージを整形する。op は操作名
// （"switch" など）で、「<op> を開始できません」を summary に、原因を err に載せる。
// switch / stop の前段（実行コンテキスト解決と名前解析）と create / remove の前段に
// 散っていた同型の整形（計 6 箇所）をここへ集約する。別名変更だけは前置きが異なるため
// 各所で個別に整形する。
func opStartFailed(op string, err error) opDoneMsg {
	return opDoneMsg{summary: op + " を開始できません", err: err}
}

// runner は TUI の各操作を同期実行し、結果を Bubble Tea のメッセージ（opDoneMsg /
// resolvedMsg / treesMsg / reposMsg）へ整形して返す継ぎ目。model はこのインターフェース
// 越しに操作を発行し、tea.Cmd クロージャは結果を運ぶだけの薄いラッパになる。キー入力から
// 実際に呼ばれる操作と引数の結線は、テストがフェイク実装を差し込んで検証する（本番の
// App 呼び出しはワークフロー側のテストが担う）。設定（cfg）は変化するため発行時にモデルが
// スナップショットして各メソッドへ渡す。ctx / root / fw はモデルの生存期間を通じて不変。
type runner interface {
	Switch(cfg *config.File, name string, restart bool) tea.Msg
	Stop(cfg *config.File, name string) tea.Msg
	Create(cfg *config.File, name string, repos []string) tea.Msg
	Remove(cfg *config.File, name string) tea.Msg
	Alias(cfg *config.File, name, label string) tea.Msg
	Doctor(cfg *config.File, fix bool) tea.Msg
	Resolve(lastCfg *config.File, prev bool, curKey string, seq uint64) tea.Msg
	Trees(cfg *config.File) tea.Msg
	Repos(cfg *config.File) tea.Msg
}

// appOps は runner の本番実装。ctx / root / fw はモデルの生存期間を通じて不変なため
// 構造体に持ち、変化する設定（cfg）だけを各メソッドが引数で受ける。各メソッドは buildApp で
// App を組み、CLI / MCP と同じワークフロー（App の型付きメソッド）を同期実行して結果を運ぶ。
type appOps struct {
	ctx  context.Context
	root statedir.Root
	fw   *forwarder
}

// Resolve は設定を読み直し、全体のサーバー状態と、選択中サーバーノードのログパス
// （Lines: 0 = パス解決のみ）を取り直す。この定期的な再解決が「表示の正」であり、MCP の
// エージェントや別の CLI が switch してもログ表示は自動で新しい対象へ追従する。seq は発行順の
// 世代番号で、全戻り値（エラー含む）に載せて古い解決の追い越しを applyResolved が弾く。
func (o appOps) Resolve(lastCfg *config.File, prev bool, curKey string, seq uint64) tea.Msg {
	warn := ""
	cfg, err := config.Load()
	if err != nil {
		// 編集途中の不正な設定で TUI を落とさない。直近の正常値で動き続ける。
		cfg, warn = lastCfg, "設定の再読み込みに失敗（直近の設定で継続）: "+err.Error()
	}
	cmd, err := serverCommand(cfg)
	if err != nil {
		return resolvedMsg{seq: seq, err: err}
	}
	a := buildApp(cfg, o.root, o.fw)
	status, err := a.ServerStatus(o.ctx, cmd)
	if err != nil {
		return resolvedMsg{seq: seq, err: err}
	}
	msg := resolvedMsg{seq: seq, cfg: cfg, status: status, warn: warn, selKey: curKey}
	if curKey == "" {
		return msg
	}
	wt, repo, srv, ok := splitKey(curKey)
	if !ok {
		// key が壊れている場合はステータスのみを返す（パスは触らない）。
		return msg
	}
	name, err := action.ParseName(wt)
	if err != nil {
		return resolvedMsg{seq: seq, err: err}
	}
	logs, err := a.ServerLogs(o.ctx, cmd, action.LogsKind{Scope: action.OneWorktree{Name: name}, Lines: 0, Prev: prev})
	if err != nil {
		return resolvedMsg{seq: seq, err: err}
	}
	// 該当 (repo, server) のエントリを探す。見つからなければ「まだ無い」扱い。
	msg.missing = true
	for _, e := range logs.Logs {
		if e.Repo == repo && e.Server == srv {
			msg.path = e.Path
			msg.missing = e.Missing
			break
		}
	}
	return msg
}

// Trees は worktree 一覧（別名・稼働サーバーつき）を取り直す。
func (o appOps) Trees(cfg *config.File) tea.Msg {
	res, err := buildApp(cfg, o.root, o.fw).List(o.ctx)
	return treesMsg{res: res, err: err}
}

// Repos は create の作成先候補（repos_dir 直下のリポジトリ）を取り直す。
func (o appOps) Repos(cfg *config.File) tea.Msg {
	res, err := buildApp(cfg, o.root, o.fw).ListRepos(o.ctx)
	return reposMsg{res: res, err: err}
}

// Switch は選択 worktree への server switch を実行する。他のフロントエンド（CLI / MCP）と
// 同じ repo 操作ロックを通るため、並行する操作とはリポジトリ単位で直列化される。
func (o appOps) Switch(cfg *config.File, name string, restart bool) tea.Msg {
	cmd, err := serverCommand(cfg)
	if err != nil {
		return opStartFailed("switch", err)
	}
	parsed, err := action.ParseName(name)
	if err != nil {
		return opStartFailed("switch", err)
	}
	res, err := buildApp(cfg, o.root, o.fw).ServerSwitch(o.ctx, cmd, action.SwitchKind{Name: parsed, Restart: restart})
	summary := fmt.Sprintf("switch %s: 対象なし", name)
	if res != nil && len(res.PerServer) > 0 {
		summary = fmt.Sprintf("switch %s: %s", name, render.SwitchSummary(res))
	}
	return opDoneMsg{summary: summary, err: err}
}

// Stop は選択 worktree のサーバー停止を実行する。
func (o appOps) Stop(cfg *config.File, name string) tea.Msg {
	cmd, err := serverCommand(cfg)
	if err != nil {
		return opStartFailed("stop", err)
	}
	parsed, err := action.ParseName(name)
	if err != nil {
		return opStartFailed("stop", err)
	}
	res, err := buildApp(cfg, o.root, o.fw).ServerStop(o.ctx, cmd, action.StopKind{Scope: action.OneWorktree{Name: parsed}})
	summary := fmt.Sprintf("stop %s: 停止対象なし", name)
	if res != nil && (res.Stopped > 0 || res.Failed > 0) {
		summary = fmt.Sprintf("stop %s: %s", name, render.StopSummary(res))
	}
	return opDoneMsg{summary: summary, err: err}
}

// Create は worktree 作成を実行する。名前は入力済みの確定値、repos はモーダルで選択された
// リポジトリ名。非対話（Selector: nil）のため、対象は明示された repos で決まる。
func (o appOps) Create(cfg *config.File, name string, repos []string) tea.Msg {
	act, err := action.NewCreate(action.CreateInput{
		Name:   name,
		Repos:  repos,
		File:   cfg,
		Getenv: os.Getenv,
		Home:   os.UserHomeDir,
	})
	if err != nil {
		return opStartFailed("create", err)
	}
	res, err := buildApp(cfg, o.root, o.fw).Create(o.ctx, act)
	summary := fmt.Sprintf("create %s: 対象なし", name)
	if res != nil {
		summary = fmt.Sprintf("create %s: %s", name, render.CreateSummary(res))
	}
	return opDoneMsg{summary: summary, err: err}
}

// Remove は worktree 削除を実行する。TUI からは常に非 --force で実行し、失敗した
// 場合の案内は「変更が残っていて削除できない場合」に限って強制削除を提示する（dirty
// の強制削除は LLM も TUI も明示コマンド経由に限る）。失敗原因はサーバー停止失敗や
// ロック競合などさまざまで、それらに対しては破壊的な --force を勧めないため、条件付き
// の文言にとどめる（原因別の分岐は行わない）。
func (o appOps) Remove(cfg *config.File, name string) tea.Msg {
	parsed, err := action.ParseName(name)
	if err != nil {
		return opStartFailed("remove", err)
	}
	_, err = buildApp(cfg, o.root, o.fw).Remove(o.ctx, action.Remove{Name: parsed, Force: false, KeepBranch: false})
	if err != nil {
		return opDoneMsg{
			summary: fmt.Sprintf("remove %s: 失敗（変更が残っていて削除できない場合は CLI: wt remove --force %s）", name, name),
			err:     err,
		}
	}
	return opDoneMsg{summary: fmt.Sprintf("remove %s: 完了", name)}
}

// Alias は worktree の表示用別名を設定・削除する。label が空なら削除、非空なら設定
// （正規化後に保存された値を summary に載せる）。
func (o appOps) Alias(cfg *config.File, name, label string) tea.Msg {
	parsed, err := action.ParseName(name)
	if err != nil {
		return opDoneMsg{summary: "別名の変更を開始できません", err: err}
	}
	a := buildApp(cfg, o.root, o.fw)
	if label == "" {
		if _, err := a.AliasRemove(o.ctx, parsed); err != nil {
			return opDoneMsg{summary: "別名を削除できません", err: err}
		}
		return opDoneMsg{summary: "別名を削除: " + name}
	}
	stored, err := a.AliasSet(o.ctx, parsed, label)
	if err != nil {
		return opDoneMsg{summary: "別名を設定できません", err: err}
	}
	return opDoneMsg{summary: fmt.Sprintf("別名を設定: %s → %s", name, stored)}
}

// Doctor は自己診断（--fix なら修復も）を実行し、結果を render.Doctor で描画したテキストと
// ともに返す。結果ペインへの表示はモデルではなくここでバッファへ描画してから運ぶ（表示層の
// 語彙を 1 箇所に保つ）。
func (o appOps) Doctor(cfg *config.File, fix bool) tea.Msg {
	res, err := buildApp(cfg, o.root, o.fw).Doctor(o.ctx, fix)
	summary := "doctor 完了"
	if fix {
		summary = "doctor --fix 完了"
	}
	var text []string
	if res != nil {
		var buf bytes.Buffer
		render.Doctor(&buf, res)
		text = strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	}
	return opDoneMsg{summary: summary, doctorText: text, err: err}
}

// 以下は model が発行する tea.Cmd の薄いラッパ。変化する設定（cfg）だけを発行時に
// スナップショットして ops へ渡し、実行は runner（本番は appOps）に委ねる。ops は不変なので
// クロージャ内から直接参照してよい。

// resolveCmd は再解決コマンドを返す。世代（resolveSeq）の前進は Update の単一 goroutine で
// 行い（ここでのインクリメントは競合しない）、以降の全戻り値にこの seq を載せる。
func (m *model) resolveCmd() tea.Cmd {
	m.log.resolveSeq++
	seq := m.log.resolveSeq
	lastCfg, prev, curKey := m.cfg, m.log.prev, m.log.curKey
	return func() tea.Msg { return m.ops.Resolve(lastCfg, prev, curKey, seq) }
}

// treesCmd は worktree 一覧を取り直すコマンドを返す。
func (m *model) treesCmd() tea.Cmd {
	cfg := m.cfg
	return func() tea.Msg { return m.ops.Trees(cfg) }
}

// reposCmd は create の作成先候補を取り直すコマンドを返す。
func (m *model) reposCmd() tea.Cmd {
	cfg := m.cfg
	return func() tea.Msg { return m.ops.Repos(cfg) }
}

// switchCmd は選択 worktree への server switch を実行するコマンドを返す。
func (m *model) switchCmd(name string, restart bool) tea.Cmd {
	cfg := m.cfg
	return func() tea.Msg { return m.ops.Switch(cfg, name, restart) }
}

// stopCmd は選択 worktree のサーバー停止を実行するコマンドを返す。
func (m *model) stopCmd(name string) tea.Cmd {
	cfg := m.cfg
	return func() tea.Msg { return m.ops.Stop(cfg, name) }
}

// createCmd は worktree 作成を実行するコマンドを返す。
func (m *model) createCmd(name string, repos []string) tea.Cmd {
	cfg := m.cfg
	return func() tea.Msg { return m.ops.Create(cfg, name, repos) }
}

// removeCmd は worktree 削除を実行するコマンドを返す。
func (m *model) removeCmd(name string) tea.Cmd {
	cfg := m.cfg
	return func() tea.Msg { return m.ops.Remove(cfg, name) }
}

// aliasCmd は worktree の表示用別名を設定・削除するコマンドを返す。
func (m *model) aliasCmd(name, label string) tea.Cmd {
	cfg := m.cfg
	return func() tea.Msg { return m.ops.Alias(cfg, name, label) }
}

// doctorCmd は自己診断（--fix なら修復も）を実行するコマンドを返す。
func (m *model) doctorCmd(fix bool) tea.Cmd {
	cfg := m.cfg
	return func() tea.Msg { return m.ops.Doctor(cfg, fix) }
}
