package tui

import (
	"bytes"
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
	"github.com/vimrak-hal/worktree-integrator/internal/infra/procctl"
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
	quiet := childio.Streams{Stdin: nil, Stdout: io.Discard, Stderr: io.Discard}
	return &app.App{
		Config:   cfg,
		Root:     root,
		ChildIO:  quiet,
		Proc:     procctl.NewUnixProcess(quiet),
		Selector: nil,
		Progress: fw,
	}
}

// serverCommand は現在の設定から server 系ワークフローの実行コンテキストを解決する
// （ディレクトリのオーバーライドは TUI に無い — 設定と WT_* 環境変数から解決する）。
func serverCommand(cfg *config.File) (action.ServerCommand, error) {
	return action.NewServerCommand(action.Overrides{}, cfg, os.Getenv, os.UserHomeDir, nil)
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

// reposCmd は create の作成先候補（repos_dir 直下のリポジトリ）を取り直すコマンドを
// 返す。
func (m *model) reposCmd() tea.Cmd {
	ctx, root, fw, cfg := m.ctx, m.root, m.fw, m.cfg
	return func() tea.Msg {
		res, err := buildApp(cfg, root, fw).ListRepos(ctx)
		return reposMsg{res: res, err: err}
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

// createCmd は worktree 作成を実行するコマンドを返す。名前は入力済みの確定値、repos は
// モーダルで選択されたリポジトリ名。非対話（Selector: nil）のため、対象は明示された
// repos で決まる。
func (m *model) createCmd(name string, repos []string) tea.Cmd {
	ctx, root, fw, cfg := m.ctx, m.root, m.fw, m.cfg
	return func() tea.Msg {
		act, err := action.NewCreate(name, repos, false, "", action.Overrides{}, cfg, os.Getenv, os.UserHomeDir)
		if err != nil {
			return opDoneMsg{summary: "create を開始できません", err: err}
		}
		res, err := buildApp(cfg, root, fw).Create(ctx, act)
		summary := fmt.Sprintf("create %s: 対象なし", name)
		if res != nil {
			summary = fmt.Sprintf("create %s: %d 作成, %d スキップ, %d 失敗",
				name, res.Created, res.Skipped, res.Failed)
		}
		return opDoneMsg{summary: summary, err: err}
	}
}

// removeCmd は worktree 削除を実行するコマンドを返す。TUI からは常に非 --force で
// 実行し、dirty で git が拒否した場合は CLI の強制削除へ誘導する（LLM も TUI も
// dirty の強制削除は明示コマンド経由に限る）。
func (m *model) removeCmd(name string) tea.Cmd {
	ctx, root, fw, cfg := m.ctx, m.root, m.fw, m.cfg
	return func() tea.Msg {
		parsed, err := action.ParseName(name)
		if err != nil {
			return opDoneMsg{summary: "remove を開始できません", err: err}
		}
		_, err = buildApp(cfg, root, fw).Remove(ctx, action.Remove{Name: parsed, Force: false, KeepBranch: false})
		if err != nil {
			return opDoneMsg{
				summary: fmt.Sprintf("remove %s: 失敗（強制削除は CLI: wt remove --force %s）", name, name),
				err:     err,
			}
		}
		return opDoneMsg{summary: fmt.Sprintf("remove %s: 完了", name)}
	}
}

// aliasCmd は worktree の表示用別名を設定・削除するコマンドを返す。label が空なら
// 削除、非空なら設定（正規化後に保存された値を summary に載せる）。
func (m *model) aliasCmd(name, label string) tea.Cmd {
	ctx, root, fw, cfg := m.ctx, m.root, m.fw, m.cfg
	return func() tea.Msg {
		parsed, err := action.ParseName(name)
		if err != nil {
			return opDoneMsg{summary: "別名の変更を開始できません", err: err}
		}
		a := buildApp(cfg, root, fw)
		if label == "" {
			if _, err := a.AliasRemove(ctx, parsed); err != nil {
				return opDoneMsg{summary: "別名を削除できません", err: err}
			}
			return opDoneMsg{summary: "別名を削除: " + name}
		}
		stored, err := a.AliasSet(ctx, parsed, label)
		if err != nil {
			return opDoneMsg{summary: "別名を設定できません", err: err}
		}
		return opDoneMsg{summary: fmt.Sprintf("別名を設定: %s → %s", name, stored)}
	}
}

// doctorCmd は自己診断（--fix なら修復も）を実行し、結果を render.Doctor で描画した
// テキストとともに返すコマンドを返す。結果ペインへの表示はモデルではなくここで
// バッファへ描画してから運ぶ（表示層の語彙を 1 箇所に保つ）。
func (m *model) doctorCmd(fix bool) tea.Cmd {
	ctx, root, fw, cfg := m.ctx, m.root, m.fw, m.cfg
	return func() tea.Msg {
		res, err := buildApp(cfg, root, fw).Doctor(ctx, fix)
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
}
