// Package clirun は CLI の Invocation を App のワークフローへ振り分け、返った型付き
// Result を adapter/render で stdout へ描画する。main はここへ配線するだけで、dispatch と
// 整形のロジックは持たない（旧 main.dispatch / dispatchServer / runConfigCheck の移設先）。
//
// エラー時の部分 Result 描画は render.Emit の 1 箇所に集約される（res が非 nil なら
// エラーの有無に関わらず描画してからエラーを返す）。これにより CLI と MCP（adapter/mcpserver
// も同じ render.Emit を経由する）で、ワークフローが部分結果を返し始めても挙動が割れない。
// 終了コード（0 / 1 / 130）の決定は main に残る（Run はエラーを返すだけ）。
package clirun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/vimrak-hal/worktree-integrator/internal/adapter/cli"
	"github.com/vimrak-hal/worktree-integrator/internal/adapter/render"
	"github.com/vimrak-hal/worktree-integrator/internal/app"
	"github.com/vimrak-hal/worktree-integrator/internal/app/action"
	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
)

// Run は Invocation を対応する App メソッドへ振り分け、返った型付き Result を render で
// stdout へ描画する。Invocation / ServerKind / AliasKind は封印された sum-type であり、
// App を必要としないバリアント（HelpShown / RunMCP / ConfigCheck / RunUI）は main が
// 先にさばくため、未知の値はバグでありパニックさせる。
func Run(ctx context.Context, inv cli.Invocation, a *app.App, stdout io.Writer) error {
	switch v := inv.(type) {
	case cli.Create:
		act, err := action.NewCreate(v.Name, v.Repos, v.All, v.Base, v.Ov, a.Config, os.Getenv, os.UserHomeDir)
		if err != nil {
			return err
		}
		res, err := a.Create(ctx, act)
		return render.Emit(stdout, res, err, render.Create)

	case cli.List:
		res, err := a.List(ctx)
		if v.Json {
			if err != nil {
				return err
			}
			return render.JSON(stdout, res)
		}
		return render.Emit(stdout, res, err, render.List)

	case cli.Enter:
		res, err := a.Enter(ctx, v.Name)
		return render.Emit(stdout, res, err, render.Enter)

	case cli.Remove:
		res, err := a.Remove(ctx, action.Remove{Name: v.Name, Force: v.Force, KeepBranch: v.KeepBranch})
		return render.Emit(stdout, res, err, render.Remove)

	case cli.Doctor:
		res, err := a.Doctor(ctx, v.Fix)
		if !v.Json {
			return render.Emit(stdout, res, err, render.Doctor)
		}
		// --json も部分結果を出す（res があれば JSON 出力し、エンコード失敗はそれを
		// 優先して返す）。テキスト経路の Emit と同じ「res があれば描画してから err」規約。
		if res != nil {
			if jsonErr := render.JSON(stdout, res); jsonErr != nil {
				return jsonErr
			}
		}
		return err

	case cli.Repos:
		res, err := a.ListRepos(ctx)
		return render.Emit(stdout, res, err, render.Repos)

	case cli.Server:
		cmd, err := action.NewServerCommand(v.Ov, a.Config, os.Getenv, os.UserHomeDir, v.Repos)
		if err != nil {
			return err
		}
		return runServer(ctx, a, v, cmd, stdout)

	case cli.Alias:
		return runAlias(ctx, a, v, stdout)

	default:
		panic(fmt.Sprintf("unknown cli.Invocation %T", inv))
	}
}

// runServer は server の操作を対応する App メソッドへ振り分ける。
func runServer(ctx context.Context, a *app.App, v cli.Server, cmd action.ServerCommand, stdout io.Writer) error {
	switch k := v.Kind.(type) {
	case action.SwitchKind:
		res, err := a.ServerSwitch(ctx, cmd, k)
		return render.Emit(stdout, res, err, render.Switch)

	case action.StatusKind:
		res, err := a.ServerStatus(ctx, cmd)
		if v.Json {
			if err != nil {
				return err
			}
			return render.JSON(stdout, res)
		}
		return render.Emit(stdout, res, err, render.Status)

	case action.StopKind:
		res, err := a.ServerStop(ctx, cmd, k)
		return render.Emit(stdout, res, err, render.Stop)

	case action.LogsKind:
		res, err := a.ServerLogs(ctx, cmd, k)
		// -f（tail -f）は CLI 専用の表示手段: ワークフローが解決したログパスを受けて
		// 自前で tail を実行する。存在しないログは追跡できないため除く。
		if v.FollowLogs {
			if err != nil {
				return err
			}
			paths := render.FollowHeader(stdout, res)
			if len(paths) == 0 {
				return nil
			}
			return cli.FollowLogs(ctx, paths, k.Lines)
		}
		return render.Emit(stdout, res, err, render.Logs)

	default:
		panic(fmt.Sprintf("unknown action.ServerKind %T", v.Kind))
	}
}

// runAlias は alias の操作を対応する App メソッドへ振り分ける。set / remove は構造化
// Result を持たずスカラー値から描画するため Emit を経由しない（list は Emit）。
func runAlias(ctx context.Context, a *app.App, v cli.Alias, stdout io.Writer) error {
	switch k := v.Kind.(type) {
	case action.AliasSet:
		stored, err := a.AliasSet(ctx, k.Name, k.Value)
		if err != nil {
			return err
		}
		render.AliasSet(stdout, k.Name.String(), stored)
		return nil
	case action.AliasList:
		res, err := a.AliasList(ctx)
		return render.Emit(stdout, res, err, render.Aliases)
	case action.AliasRemove:
		existed, err := a.AliasRemove(ctx, k.Name)
		if err != nil {
			return err
		}
		render.AliasRemoved(stdout, k.Name.String(), existed)
		return nil
	default:
		panic(fmt.Sprintf("unknown action.AliasKind %T", v.Kind))
	}
}

// ConfigCheck は `wt config check` を配線する。設定ファイルの既定パスを解決し、
// core/config.Check で不存在・正常・不正の 3 通りを判定して render.ConfigCheck で
// 描画する。見つからない・正常は nil、不正は検証エラーを返す（main が stderr へ書き、
// exit 1 へ写す）。App を必要としないため main が App 構築の手前で呼ぶ。
func ConfigCheck(stdout io.Writer) error {
	path, ok := config.DefaultPath()
	if !ok {
		return errors.New("設定ファイルの既定パスを解決できません（ホームディレクトリを特定できません）")
	}
	return render.ConfigCheck(stdout, config.Check(path))
}
