package tui

import (
	"fmt"
	"io"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

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

// resolvedMsg は resolveCmd の結果: 再読み込み済みの設定と、ログ対象（パス解決
// のみ・本文は tailer が読む）・サーバー状態。
type resolvedMsg struct {
	cfg    *config.File
	logs   *server.LogsResult
	status *server.StatusResult
	warn   string
	err    error
}

// treesMsg は treesCmd の結果（worktree 一覧）。
type treesMsg struct {
	res *tree.ListResult
	err error
}

// serverEventMsg は switch / stop 実行中のライフサイクルイベント 1 件
// （forwarder がワークフローの goroutine から送る）。
type serverEventMsg struct{ line string }

// opDoneMsg は switch / stop の完了。summary は日本語の 1 行サマリで、err が
// 非 nil でも部分的な成功を含みうる（ワークフローの規約どおり）。
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
// app.Progress の実装。ワークフローは Update の外（tea.Cmd の goroutine）で走るため、
// モデルを直接触らず p.Send で正規のメッセージ経路に乗せる。p は Run が
// tea.NewProgram の直後（ユーザー操作が始まる前）に設定する。
type forwarder struct{ p *tea.Program }

// Update / Event は worktree 作成の進捗（TUI からは実行しないため未使用）。
func (f *forwarder) Update(string, worktree.Progress) {}
func (f *forwarder) Event(string, worktree.Note)      {}

// ServerEvent はサーバーのライフサイクルイベントを転送する。
func (f *forwarder) ServerEvent(repo, srv string, ev coreserver.Event) {
	if f.p != nil {
		f.p.Send(serverEventMsg{line: eventLine(repo+"/"+srv, ev)})
	}
}

// eventLine は 1 つのサーバーイベントをイベント履歴の 1 行に整形する
// （render.serverEventLine と同じ語彙の TUI 向け短縮形）。EventKind は封印された
// 列挙のため、未知の値はバグでありパニックさせる。
func eventLine(tag string, ev coreserver.Event) string {
	switch ev.Kind {
	case coreserver.EventAlreadyRunning:
		return fmt.Sprintf("[%s] 既に起動中 (pid %d)", tag, ev.Pid)
	case coreserver.EventStoppingOld:
		return fmt.Sprintf("[%s] 旧サーバー停止 (pid %d)", tag, ev.Pid)
	case coreserver.EventStarted:
		return fmt.Sprintf("[%s] 起動 (pid %d)", tag, ev.Pid)
	case coreserver.EventStopped:
		return fmt.Sprintf("[%s] 停止 (pid %d)", tag, ev.Pid)
	case coreserver.EventStopFailed:
		return fmt.Sprintf("[%s] 停止失敗 (pid %d): %v", tag, ev.Pid, ev.Err)
	case coreserver.EventAlreadyStopped:
		return fmt.Sprintf("[%s] 既に停止済み（記録を消去）", tag)
	default:
		panic(fmt.Sprintf("unknown coreserver.EventKind %d", ev.Kind))
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

// logsScope は現在の worktree 絞り込みを action の語彙へ写す。
func logsScope(scope string) (action.WorktreeScope, error) {
	if scope == "" {
		return action.AllWorktrees{}, nil
	}
	name, err := action.ParseName(scope)
	if err != nil {
		return nil, err
	}
	return action.OneWorktree{Name: name}, nil
}

// resolveCmd は設定を読み直し、ログ対象のパス（Lines: 0 = パス解決のみ）とサーバー
// 状態を取り直すコマンドを返す。この定期的な再解決が「表示の正」であり、MCP の
// エージェントや別の CLI が switch してもログ表示は自動で新しい対象へ追従する。
func (m *model) resolveCmd() tea.Cmd {
	ctx, root, fw := m.ctx, m.root, m.fw
	lastCfg, scope, prev := m.cfg, m.scope, m.prev
	return func() tea.Msg {
		warn := ""
		cfg, err := config.Load()
		if err != nil {
			// 編集途中の不正な設定で TUI を落とさない。直近の正常値で動き続ける。
			cfg, warn = lastCfg, "設定の再読み込みに失敗（直近の設定で継続）: "+err.Error()
		}
		cmd, err := serverCommand(cfg)
		if err != nil {
			return resolvedMsg{err: err}
		}
		sc, err := logsScope(scope)
		if err != nil {
			return resolvedMsg{err: err}
		}
		a := buildApp(cfg, root, fw)
		logs, err := a.ServerLogs(ctx, cmd, action.LogsKind{Scope: sc, Lines: 0, Prev: prev})
		if err != nil {
			return resolvedMsg{err: err}
		}
		status, err := a.ServerStatus(ctx, cmd)
		if err != nil {
			return resolvedMsg{err: err}
		}
		return resolvedMsg{cfg: cfg, logs: logs, status: status, warn: warn}
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
