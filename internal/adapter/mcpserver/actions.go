package mcpserver

import (
	"bytes"
	"context"
	"errors"
	"os"

	"github.com/vimrak-hal/worktree-integrator/internal/adapter/render"
	"github.com/vimrak-hal/worktree-integrator/internal/app"
	"github.com/vimrak-hal/worktree-integrator/internal/app/create"
	"github.com/vimrak-hal/worktree-integrator/internal/app/server"
	"github.com/vimrak-hal/worktree-integrator/internal/app/tree"
	"github.com/vimrak-hal/worktree-integrator/internal/core/action"
	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
	coreserver "github.com/vimrak-hal/worktree-integrator/internal/core/server"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/childio"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/statedir"
)

// ----- パラメータスキーマ -----
//
// かつて全ツールが受けていた repos_dir / worktrees_dir のオーバーライドは廃止した
// （意図的な仕様変更）。ディレクトリは設定ファイル（と MCP サーバープロセスの環境
// 変数）を唯一の真実とし、LLM がツール呼び出しごとに別のディレクトリを指す経路を
// 塞ぐ。

// CreateParams は worktree_create のパラメータ。
type CreateParams struct {
	WorktreeName string   `json:"worktree_name" jsonschema:"The worktree name, also used as the branch name. Letters, digits, '.', '_' and hyphens, optionally split into segments by '/' for a hierarchical name (e.g. feature/login). Segments must not be '.'/'..', start with '-' or '.', or end with '.lock'."`
	Repos        []string `json:"repos" jsonschema:"The repository names to create the worktree in (see repos_list). Must be non-empty; unknown names are an error."`
	Base         string   `json:"base,omitempty" jsonschema:"Override the base branch/ref to create from (defaults to repos.<repo>.base / defaults.base / auto-detecting the remote's default branch)."`
	Remote       string   `json:"remote,omitempty" jsonschema:"Override the remote to fetch from (defaults to origin)."`
	Concurrency  int      `json:"concurrency,omitempty" jsonschema:"Cap the number of repositories processed in parallel (0 = automatic)."`
}

// ServerSwitchParams は server_switch のパラメータ。
type ServerSwitchParams struct {
	WorktreeName    string   `json:"worktree_name" jsonschema:"The worktree whose servers to activate."`
	Repos           []string `json:"repos,omitempty" jsonschema:"Limit to these repositories; empty means every configured repository."`
	RequireWorktree bool     `json:"require_worktree,omitempty" jsonschema:"Error (instead of skipping) when a repository's worktree is missing."`
	Restart         bool     `json:"restart,omitempty" jsonschema:"Restart even if the requested worktree's server is already running."`
}

// ServerScopeParams は server_status のパラメータ。
type ServerScopeParams struct {
	Repos []string `json:"repos,omitempty" jsonschema:"Limit to these repositories; empty means every configured repository."`
}

// ServerStopParams は server_stop のパラメータ。
type ServerStopParams struct {
	WorktreeName *string  `json:"worktree_name,omitempty" jsonschema:"Only stop servers running for this worktree; omit to stop all. An empty string is an error, not 'all'."`
	Repos        []string `json:"repos,omitempty" jsonschema:"Limit to these repositories; empty means every configured repository."`
}

// ServerLogsParams は server_logs のパラメータ。
type ServerLogsParams struct {
	WorktreeName *string  `json:"worktree_name,omitempty" jsonschema:"View this worktree's logs; omit for the currently running servers'. An empty string is an error, not 'all'."`
	Repos        []string `json:"repos,omitempty" jsonschema:"Limit to these repositories; empty means every configured repository."`
	Lines        int      `json:"lines,omitempty" jsonschema:"Number of trailing lines to show (default 50, clamped to at most 2000)."`
	Prev         bool     `json:"prev,omitempty" jsonschema:"Read the previous generation of the log instead (rotated aside when the server was last started)."`
}

// WorktreeRemoveParams は worktree_remove のパラメータ。CLI の --force に相当する
// パラメータは意図的に存在しない: dirty なチェックアウトはエラーとして返るのみで、
// LLM に強制削除を許さない（意図的な非公開）。
type WorktreeRemoveParams struct {
	Name       string `json:"name" jsonschema:"The worktree name to remove (see worktree_list)."`
	KeepBranch bool   `json:"keep_branch,omitempty" jsonschema:"Keep the branch instead of deleting it along with the worktree."`
}

// AliasSetParams は alias_set のパラメータ。
type AliasSetParams struct {
	WorktreeName string `json:"worktree_name" jsonschema:"The worktree (and branch) name the alias is keyed by."`
	Alias        string `json:"alias" jsonschema:"The label to display. Must be non-empty; use alias_remove to clear."`
}

// AliasNameParams は alias_remove のパラメータ。
type AliasNameParams struct {
	WorktreeName string `json:"worktree_name" jsonschema:"The worktree name to look up."`
}

// NoParams は repos_list / alias_list 用の空のパラメータセット。
type NoParams struct{}

// ----- 共通ヘルパー -----

// toolApp はツール呼び出し 1 回分の App と取り込みバッファの組である。ワークフローの
// 途中経過（Progress）と最終 Result の描画（render）が同じバッファに集まり、その
// 内容がツール結果の人間向けテキストになる。
type toolApp struct {
	app *app.App
	buf bytes.Buffer
}

// newToolApp はツール呼び出し 1 回分の App を構築する。MCP サーバーは長寿命のため、
// 設定は（プロセス起動時に 1 回ではなく）ツール呼び出しのたびに読み直し、設定
// ファイルの編集が再起動なしで反映されるようにする。
func newToolApp() (*toolApp, error) {
	file, err := config.Load()
	if err != nil {
		return nil, err
	}
	root, err := statedir.Default()
	if err != nil {
		return nil, err
	}
	t := &toolApp{}
	t.app = &app.App{
		Config:  file,
		Root:    root,
		ChildIO: childio.Quiet(),
		Proc:    coreserver.NewUnixProcess(childio.Quiet()),
		// Selector は nil（非対話）。対話選択モードに入る呼び出しはエラーになる。
		Selector: nil,
		Progress: render.NewProgress(&t.buf),
	}
	return t, nil
}

// text は取り込みバッファの現在の内容を返す。
func (t *toolApp) text() string { return t.buf.String() }

// serverLogsLineLimit は server_logs が一度に返す最大行数。MCP クライアントの
// コンテキストを巨大なログで溢れさせないための上限である。
const serverLogsLineLimit = 2000

// defaultLogLines は lines 省略時の既定行数。
const defaultLogLines = 50

// ----- アクションのグルーコード -----
//
// 各アクションは（型付き Result・人間向けテキスト・エラー）を返し、handle が
// structuredContent と TextContent へ写す。

func actionReposList(ctx context.Context, _ NoParams) (*app.ReposResult, string, error) {
	t, err := newToolApp()
	if err != nil {
		return nil, "", err
	}
	res, err := t.app.ListRepos(ctx)
	if err != nil {
		return nil, t.text(), err
	}
	render.Repos(&t.buf, res)
	return res, t.text(), nil
}

func actionCreateWorktrees(ctx context.Context, params CreateParams) (*create.Result, string, error) {
	if len(params.Repos) == 0 {
		return nil, "", errors.New("`repos` を 1 つ以上指定してください（候補は repos_list で取得できます）")
	}
	t, err := newToolApp()
	if err != nil {
		return nil, "", err
	}
	act, err := action.NewCreate(params.WorktreeName, params.Repos, false, params.Base, action.Overrides{
		Remote:      params.Remote,
		Concurrency: params.Concurrency,
	}, t.app.Config, os.Getenv)
	if err != nil {
		return nil, "", err
	}

	// 明示されたリポジトリ名の実在検証は app/create 内の 1 箇所で行われる
	//（未知の名前はエラー）。CLI の --repo と同じ経路である。
	res, runErr := t.app.Create(ctx, act)
	if res != nil {
		render.Create(&t.buf, res)
	}
	return res, t.text(), runErr
}

func actionWorktreeList(ctx context.Context, _ NoParams) (*tree.ListResult, string, error) {
	t, err := newToolApp()
	if err != nil {
		return nil, "", err
	}
	res, runErr := t.app.List(ctx)
	if res != nil {
		render.List(&t.buf, res)
	}
	return res, t.text(), runErr
}

func actionWorktreeRemove(ctx context.Context, params WorktreeRemoveParams) (*tree.RemoveResult, string, error) {
	name, err := action.ParseName(params.Name)
	if err != nil {
		return nil, "", err
	}
	t, err := newToolApp()
	if err != nil {
		return nil, "", err
	}
	// Force は常に false: dirty なチェックアウトの削除拒否（git の安全弁）を MCP から
	// 上書きする経路は存在しない。
	res, runErr := t.app.Remove(ctx, action.Remove{Name: name, KeepBranch: params.KeepBranch})
	if res != nil {
		render.Remove(&t.buf, res)
	}
	return res, t.text(), runErr
}

func actionServerSwitch(ctx context.Context, params ServerSwitchParams) (*server.SwitchResult, string, error) {
	name, err := action.ParseName(params.WorktreeName)
	if err != nil {
		return nil, "", err
	}
	t, cmd, err := serverCommand(params.Repos)
	if err != nil {
		return nil, "", err
	}
	res, runErr := t.app.ServerSwitch(ctx, cmd, action.SwitchKind{
		Name:            name,
		RequireWorktree: params.RequireWorktree,
		Restart:         params.Restart,
	})
	if res != nil {
		render.Switch(&t.buf, res)
	}
	return res, t.text(), runErr
}

func actionServerStatus(ctx context.Context, params ServerScopeParams) (*server.StatusResult, string, error) {
	t, cmd, err := serverCommand(params.Repos)
	if err != nil {
		return nil, "", err
	}
	res, runErr := t.app.ServerStatus(ctx, cmd)
	if res != nil {
		render.Status(&t.buf, res)
	}
	return res, t.text(), runErr
}

func actionServerStop(ctx context.Context, params ServerStopParams) (*server.StopResult, string, error) {
	scope, err := scopeFromPtr(params.WorktreeName)
	if err != nil {
		return nil, "", err
	}
	t, cmd, err := serverCommand(params.Repos)
	if err != nil {
		return nil, "", err
	}
	res, runErr := t.app.ServerStop(ctx, cmd, action.StopKind{Scope: scope})
	if res != nil {
		render.Stop(&t.buf, res)
	}
	return res, t.text(), runErr
}

func actionServerLogs(ctx context.Context, params ServerLogsParams) (*server.LogsResult, string, error) {
	scope, err := scopeFromPtr(params.WorktreeName)
	if err != nil {
		return nil, "", err
	}
	t, cmd, err := serverCommand(params.Repos)
	if err != nil {
		return nil, "", err
	}
	// フォロー（tail -f）は action.LogsKind に存在しないため、MCP から到達する
	// 経路は型レベルで存在しない。
	res, runErr := t.app.ServerLogs(ctx, cmd, action.LogsKind{
		Scope: scope,
		Lines: clampLines(params.Lines),
		Prev:  params.Prev,
	})
	if res != nil {
		render.Logs(&t.buf, res)
	}
	return res, t.text(), runErr
}

func actionAliasSet(ctx context.Context, params AliasSetParams) (string, error) {
	name, err := action.ParseName(params.WorktreeName)
	if err != nil {
		return "", err
	}
	t, err := newToolApp()
	if err != nil {
		return "", err
	}
	stored, err := t.app.AliasSet(ctx, name, params.Alias)
	if err != nil {
		return "", err
	}
	render.AliasSet(&t.buf, name.String(), stored)
	return t.text(), nil
}

func actionAliasList(ctx context.Context, _ NoParams) (*app.AliasesResult, string, error) {
	t, err := newToolApp()
	if err != nil {
		return nil, "", err
	}
	res, err := t.app.AliasList(ctx)
	if err != nil {
		return nil, "", err
	}
	render.Aliases(&t.buf, res)
	return res, t.text(), nil
}

func actionAliasRemove(ctx context.Context, params AliasNameParams) (string, error) {
	name, err := action.ParseName(params.WorktreeName)
	if err != nil {
		return "", err
	}
	t, err := newToolApp()
	if err != nil {
		return "", err
	}
	existed, err := t.app.AliasRemove(ctx, name)
	if err != nil {
		return "", err
	}
	render.AliasRemoved(&t.buf, name.String(), existed)
	return t.text(), nil
}

// serverCommand は server 系ツール共通の App とコマンドコンテキストを構築する。
func serverCommand(repos []string) (*toolApp, action.ServerCommand, error) {
	t, err := newToolApp()
	if err != nil {
		return nil, action.ServerCommand{}, err
	}
	cmd, err := action.NewServerCommand(action.Overrides{}, t.app.Config, os.Getenv, repos)
	if err != nil {
		return nil, action.ServerCommand{}, err
	}
	return t, cmd, nil
}

// clampLines は server_logs の行数を [既定 50, 上限 2000] に収める。0 以下
// （省略を含む）は既定へ、上限超過は上限へクランプする。
func clampLines(lines int) int {
	if lines <= 0 {
		return defaultLogLines
	}
	return min(lines, serverLogsLineLimit)
}

// scopeFromPtr は省略可能な worktree 名ポインタを WorktreeScope に変換する。
// nil（パラメータ省略）のみが「全 worktree 対象」を意味する。明示的な空文字列は
// （旧実装のように全件へ正規化せず）不正な名前としてエラーになる — 意図的な仕様変更。
// LLM が「空文字列 = 全件」という暗黙の規約に頼ることを許さない。
func scopeFromPtr(p *string) (action.WorktreeScope, error) {
	if p == nil {
		return action.AllWorktrees{}, nil
	}
	name, err := action.ParseName(*p)
	if err != nil {
		return nil, err
	}
	return action.OneWorktree{Name: name}, nil
}
