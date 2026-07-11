package tui

import (
	"io"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vimrak-hal/worktree-integrator/internal/app"
	"github.com/vimrak-hal/worktree-integrator/internal/app/server"
	"github.com/vimrak-hal/worktree-integrator/internal/app/tree"
	"github.com/vimrak-hal/worktree-integrator/internal/core/action"
	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
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

func tailTick() tea.Cmd {
	return tea.Tick(tailInterval, func(t time.Time) tea.Msg { return tailTickMsg(t) })
}

func resolveTick() tea.Cmd {
	return tea.Tick(resolveInterval, func(t time.Time) tea.Msg { return resolveTickMsg(t) })
}

func treesTick() tea.Cmd {
	return tea.Tick(treesInterval, func(t time.Time) tea.Msg { return treesTickMsg(t) })
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

// resolveCmd は設定を読み直し、全体のサーバー状態と、選択中サーバーノードのログパス
// （Lines: 0 = パス解決のみ）を取り直すコマンドを返す。この定期的な再解決が「表示の
// 正」であり、MCP のエージェントや別の CLI が switch してもログ表示は自動で新しい
// 対象へ追従する。
func (m *model) resolveCmd() tea.Cmd {
	// 発行のたびに世代を進めて捕捉する。Update は単一 goroutine で走るためここでの
	// インクリメントは競合しない。以降の全戻り値（エラー含む）にこの seq を載せる。
	m.resolveSeq++
	seq := m.resolveSeq
	ctx, root := m.ctx, m.root
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
		a := buildApp(cfg, root)
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
	ctx, root, cfg := m.ctx, m.root, m.cfg
	return func() tea.Msg {
		res, err := buildApp(cfg, root).List(ctx)
		return treesMsg{res: res, err: err}
	}
}
