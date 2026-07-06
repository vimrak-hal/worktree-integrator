package tui

import (
	"io"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vimrak-hal/worktree-integrator/internal/app"
	"github.com/vimrak-hal/worktree-integrator/internal/app/server"
	"github.com/vimrak-hal/worktree-integrator/internal/core/action"
	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
	coreserver "github.com/vimrak-hal/worktree-integrator/internal/core/server"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/childio"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/statedir"
)

// ポーリング間隔。tail は増分読みだけなので速く、resolve は状態ファイルロックを
// 伴うため控えめにする。resolve の再解決が MCP・別 CLI による並行変更（switch
// など）を拾う経路である。
const (
	tailInterval    = 500 * time.Millisecond
	resolveInterval = 2 * time.Second
)

type (
	tailTickMsg    time.Time
	resolveTickMsg time.Time
)

// resolvedMsg は resolveCmd の結果: 再読み込み済みの設定と、ログ対象（パス解決
// のみ・本文は tailer が読む）。
type resolvedMsg struct {
	cfg  *config.File
	logs *server.LogsResult
	warn string
	err  error
}

func tailTick() tea.Cmd {
	return tea.Tick(tailInterval, func(t time.Time) tea.Msg { return tailTickMsg(t) })
}

func resolveTick() tea.Cmd {
	return tea.Tick(resolveInterval, func(t time.Time) tea.Msg { return resolveTickMsg(t) })
}

// buildApp は TUI 配下でワークフローを駆動する App を組み立てる。子プロセス
// （setup / on_switch などのライフサイクルコマンド）の出力は破棄する: TUI が端末を
// 専有しているため、stdout / stderr へ書くと画面が壊れる（MCP の childio.Quiet と
// 同じ理由の TUI 版）。失敗の内容は型付きの Failure（起動失敗ならログ末尾つき）で
// 報告される。
func buildApp(cfg *config.File, root statedir.Root) *app.App {
	quiet := childio.Streams{Stdin: nil, Stdout: io.Discard, Stderr: io.Discard}
	return &app.App{
		Config:   cfg,
		Root:     root,
		ChildIO:  quiet,
		Proc:     coreserver.NewUnixProcess(quiet),
		Selector: nil,
		Progress: nil,
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

// resolveCmd は設定を読み直し、ログ対象のパス（Lines: 0 = パス解決のみ）を取り直す
// コマンドを返す。この定期的な再解決が「表示の正」であり、MCP のエージェントや
// 別の CLI が switch してもログ表示は自動で新しい対象へ追従する。
func (m *model) resolveCmd() tea.Cmd {
	ctx, root := m.ctx, m.root
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
		logs, err := buildApp(cfg, root).ServerLogs(ctx, cmd, action.LogsKind{Scope: sc, Lines: 0, Prev: prev})
		if err != nil {
			return resolvedMsg{err: err}
		}
		return resolvedMsg{cfg: cfg, logs: logs, warn: warn}
	}
}
