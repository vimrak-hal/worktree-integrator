package mcpserver

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"

	"github.com/vimrak-hal/worktree-integrator/internal/adapter/render"
	"github.com/vimrak-hal/worktree-integrator/internal/app"
	"github.com/vimrak-hal/worktree-integrator/internal/app/action"
	"github.com/vimrak-hal/worktree-integrator/internal/app/create"
	"github.com/vimrak-hal/worktree-integrator/internal/app/server"
	"github.com/vimrak-hal/worktree-integrator/internal/app/tree"
	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/childio"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/statedir"
)

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
	// Selector は既定の nil（非対話）: 対話選択モードに入る呼び出しはエラーになる。
	// Progress は取り込みバッファへ描画する。
	t.app = app.New(file, root, childio.Quiet(), app.WithProgress(render.NewProgress(&t.buf)))
	return t, nil
}

// text は取り込みバッファの現在の内容を返す。
func (t *toolApp) text() string { return t.buf.String() }

// run は構造化 Result を返すツールアクション共通のグルーを畳む: toolApp を構築し、
// call でワークフローを駆動して型付き Result を得て、その Result を render.Emit 経由で
// 取り込みバッファに描画してから (Result, テキスト, エラー) を返す。render.Emit は
// 「res が非 nil ならエラーの有無に関わらず描画してから err を返す」規約の単一実装点で
// あり（CLI の adapter/clirun と共有）、部分 Result の描画挙動が両フロントエンドで
// 一致することを保証する。call が返すエラー（設定解決やコマンド構築の失敗を含む）は
// そのまま伝播する。
func run[R any](call func(*app.App) (*R, error), draw func(io.Writer, *R)) (*R, string, error) {
	t, err := newToolApp()
	if err != nil {
		return nil, "", err
	}
	res, runErr := call(t.app)
	// text() より先に Emit を評価する（引数の評価順で text がバッファを先読みしないよう、
	// 描画を確定させてから取り込む）。
	outErr := render.Emit(&t.buf, res, runErr, draw)
	return res, t.text(), outErr
}

// ----- アクションのグルーコード -----
//
// 各アクションは（型付き Result・人間向けテキスト・エラー）を返し、handle が
// structuredContent と TextContent へ写す。

func actionReposList(ctx context.Context, _ NoParams) (*app.ReposResult, string, error) {
	return run(func(a *app.App) (*app.ReposResult, error) {
		return a.ListRepos(ctx)
	}, render.Repos)
}

func actionCreateWorktrees(ctx context.Context, params CreateParams) (*create.Result, string, error) {
	if len(params.Repos) == 0 {
		return nil, "", errors.New("`repos` を 1 つ以上指定してください（候補は repos_list で取得できます）")
	}
	return run(func(a *app.App) (*create.Result, error) {
		act, err := action.NewCreate(action.CreateInput{
			Name:  params.WorktreeName,
			Repos: params.Repos,
			Base:  params.Base,
			Overrides: action.Overrides{
				Remote:      params.Remote,
				Concurrency: params.Concurrency,
			},
			File:   a.Config,
			Getenv: os.Getenv,
			Home:   os.UserHomeDir,
		})
		if err != nil {
			return nil, err
		}
		// 明示されたリポジトリ名の実在検証は app/create 内の 1 箇所で行われる
		//（未知の名前はエラー）。CLI の --repo と同じ経路である。
		return a.Create(ctx, act)
	}, render.Create)
}

func actionWorktreeList(ctx context.Context, _ NoParams) (*tree.ListResult, string, error) {
	return run(func(a *app.App) (*tree.ListResult, error) {
		return a.List(ctx)
	}, render.List)
}

func actionWorktreeRemove(ctx context.Context, params WorktreeRemoveParams) (*tree.RemoveResult, string, error) {
	name, err := action.ParseName(params.Name)
	if err != nil {
		return nil, "", err
	}
	return run(func(a *app.App) (*tree.RemoveResult, error) {
		// Force は常に false: dirty なチェックアウトの削除拒否（git の安全弁）を MCP から
		// 上書きする経路は存在しない。
		return a.Remove(ctx, action.Remove{Name: name, KeepBranch: params.KeepBranch})
	}, render.Remove)
}

func actionServerSwitch(ctx context.Context, params ServerSwitchParams) (*server.SwitchResult, string, error) {
	name, err := action.ParseName(params.WorktreeName)
	if err != nil {
		return nil, "", err
	}
	return run(func(a *app.App) (*server.SwitchResult, error) {
		cmd, err := serverCommand(a, params.Repos)
		if err != nil {
			return nil, err
		}
		return a.ServerSwitch(ctx, cmd, action.SwitchKind{
			Name:            name,
			RequireWorktree: params.RequireWorktree,
			Restart:         params.Restart,
		})
	}, render.Switch)
}

func actionServerStatus(ctx context.Context, params ServerScopeParams) (*server.StatusResult, string, error) {
	return run(func(a *app.App) (*server.StatusResult, error) {
		cmd, err := serverCommand(a, params.Repos)
		if err != nil {
			return nil, err
		}
		return a.ServerStatus(ctx, cmd)
	}, render.Status)
}

func actionServerStop(ctx context.Context, params ServerStopParams) (*server.StopResult, string, error) {
	scope, err := scopeFromPtr(params.WorktreeName)
	if err != nil {
		return nil, "", err
	}
	return run(func(a *app.App) (*server.StopResult, error) {
		cmd, err := serverCommand(a, params.Repos)
		if err != nil {
			return nil, err
		}
		return a.ServerStop(ctx, cmd, action.StopKind{Scope: scope})
	}, render.Stop)
}

func actionServerLogs(ctx context.Context, params ServerLogsParams) (*server.LogsResult, string, error) {
	scope, err := scopeFromPtr(params.WorktreeName)
	if err != nil {
		return nil, "", err
	}
	return run(func(a *app.App) (*server.LogsResult, error) {
		cmd, err := serverCommand(a, params.Repos)
		if err != nil {
			return nil, err
		}
		// フォロー（tail -f）は action.LogsKind に存在しないため、MCP から到達する
		// 経路は型レベルで存在しない。
		return a.ServerLogs(ctx, cmd, action.LogsKind{
			Scope: scope,
			Lines: clampLines(params.Lines),
			Prev:  params.Prev,
		})
	}, render.Logs)
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
	return run(func(a *app.App) (*app.AliasesResult, error) {
		return a.AliasList(ctx)
	}, render.Aliases)
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

// serverCommand は server 系ツール共通のコマンドコンテキストを、ビルド済みの App の
// 設定から構築する。
func serverCommand(a *app.App, repos []string) (action.ServerCommand, error) {
	return action.NewServerCommand(action.ServerCommandInput{
		File:   a.Config,
		Getenv: os.Getenv,
		Home:   os.UserHomeDir,
		Repos:  repos,
	})
}
