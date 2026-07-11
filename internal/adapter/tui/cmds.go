package tui

import (
	"fmt"
	"io"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vimrak-hal/worktree-integrator/internal/adapter/render"
	"github.com/vimrak-hal/worktree-integrator/internal/app"
	"github.com/vimrak-hal/worktree-integrator/internal/app/server"
	"github.com/vimrak-hal/worktree-integrator/internal/app/tree"
	"github.com/vimrak-hal/worktree-integrator/internal/core/action"
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

// eventMsg はイベント履歴の 1 行。サーバーのライフサイクルイベントと worktree 作成の
// 進捗（fetch / 作成 / 型付き Note）の両方がこの 1 経路で流れる。ワークフローは
// Update の外（tea.Cmd の goroutine）で走るため、forwarder がモデルを直接触らず
// p.Send で正規のメッセージ経路に乗せる。
type eventMsg struct{ line string }

// opDoneMsg は統合操作（switch / stop）の完了。
// summary は日本語の 1 行サマリで、err が非 nil でも部分的な成功を含みうる。
type opDoneMsg struct {
	summary string
	err     error
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
// 設定する。サーバーイベント（ServerEvent）を短い日本語 1 行に整形して eventMsg で送る。
type forwarder struct{ p *tea.Program }

// Update は worktree 作成の進捗遷移の転送口（作成は未統合のため未使用）。
func (f *forwarder) Update(repo string, state worktree.Progress) {}

// Event は worktree 作成の型付き途中経過イベントの転送口（作成は未統合のため未使用）。
func (f *forwarder) Event(repo string, n worktree.Note) {}

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
	quiet := childio.Streams{Stdin: nil, Stdout: io.Discard, Stderr: io.Discard}
	return &app.App{
		Config:   cfg,
		Root:     root,
		ChildIO:  quiet,
		Proc:     coreserver.NewUnixProcess(quiet),
		Selector: nil,
		Progress: fw,
	}
}

// serverCommand は現在の設定から server 系ワークフローの実行コンテキストを解決する
// （ディレクトリのオーバーライドは TUI に無い — 設定と WT_* 環境変数から解決する）。
func serverCommand(cfg *config.File) (action.ServerCommand, error) {
	return action.NewServerCommand(action.Overrides{}, cfg, os.Getenv, nil)
}

// resolveCmd は設定を読み直し、全体のサーバー状態と、選択中サーバーノードのログパス
// （Lines: 0 = パス解決のみ）を取り直すコマンドを返す。この定期的な再解決が「表示の
// 正」であり、MCP のエージェントや別の CLI が switch してもログ表示は自動で新しい
// 対象へ追従する。
func (m *model) resolveCmd() tea.Cmd {
	// 発行のたびに世代を進めて捕捉する。Update は単一 goroutine で走るためここでの
	// インクリメントは競合しない。以降の全戻り値（エラー含む）にこの seq を載せる。
	m.resolveSeq++
	seq := m.resolveSeq
	ctx, root, fw := m.ctx, m.root, m.fw
	lastCfg, prev, curKey := m.cfg, m.prev, m.curKey
	return func() tea.Msg {
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
		a := buildApp(cfg, root, fw)
		status, err := a.ServerStatus(ctx, cmd)
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
		logs, err := a.ServerLogs(ctx, cmd, action.LogsKind{Scope: action.OneWorktree{Name: name}, Lines: 0, Prev: prev})
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
}

// treesCmd は worktree 一覧（別名・稼働サーバーつき）を取り直すコマンドを返す。
func (m *model) treesCmd() tea.Cmd {
	ctx, root, fw, cfg := m.ctx, m.root, m.fw, m.cfg
	return func() tea.Msg {
		res, err := buildApp(cfg, root, fw).List(ctx)
		return treesMsg{res: res, err: err}
	}
}

// switchCmd は選択 worktree への server switch を実行するコマンドを返す。他の
// フロントエンド（CLI / MCP）と同じ repo 操作ロックを通るため、並行する操作とは
// リポジトリ単位で直列化される。
func (m *model) switchCmd(name string, restart bool) tea.Cmd {
	ctx, root, fw, cfg := m.ctx, m.root, m.fw, m.cfg
	return func() tea.Msg {
		cmd, err := serverCommand(cfg)
		if err != nil {
			return opDoneMsg{summary: "switch を開始できません", err: err}
		}
		parsed, err := action.ParseName(name)
		if err != nil {
			return opDoneMsg{summary: "switch を開始できません", err: err}
		}
		res, err := buildApp(cfg, root, fw).ServerSwitch(ctx, cmd, action.SwitchKind{Name: parsed, Restart: restart})
		summary := fmt.Sprintf("switch %s: 対象なし", name)
		if res != nil && len(res.PerServer) > 0 {
			summary = fmt.Sprintf("switch %s: %d 起動, %d 既起動, %d スキップ, %d 失敗",
				name, res.Started, res.Already, res.Skipped, res.Failed)
		}
		return opDoneMsg{summary: summary, err: err}
	}
}

// stopCmd は選択 worktree のサーバー停止を実行するコマンドを返す。
func (m *model) stopCmd(name string) tea.Cmd {
	ctx, root, fw, cfg := m.ctx, m.root, m.fw, m.cfg
	return func() tea.Msg {
		cmd, err := serverCommand(cfg)
		if err != nil {
			return opDoneMsg{summary: "stop を開始できません", err: err}
		}
		parsed, err := action.ParseName(name)
		if err != nil {
			return opDoneMsg{summary: "stop を開始できません", err: err}
		}
		res, err := buildApp(cfg, root, fw).ServerStop(ctx, cmd, action.StopKind{Scope: action.OneWorktree{Name: parsed}})
		summary := fmt.Sprintf("stop %s: 停止対象なし", name)
		if res != nil && (res.Stopped > 0 || res.Failed > 0) {
			summary = fmt.Sprintf("stop %s: %d 停止, %d 失敗", name, res.Stopped, res.Failed)
		}
		return opDoneMsg{summary: summary, err: err}
	}
}
