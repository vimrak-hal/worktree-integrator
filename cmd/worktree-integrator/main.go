// Command worktree-integrator は、複数のリポジトリにまたがる Git ワークツリーを
// 一度に作成し、それらの開発サーバーを管理する。
//
// ワークツリー名が与えられると、このツールはリポジトリディレクトリ（デフォルトでは
// ~/repositories）配下の Git リポジトリをインタラクティブなチェックボックスリストとして
// 提示し（--repo / --all で非対話指定も可能）、選択された各リポジトリについて最新の
// リモート main ブランチをフェッチして、ワークツリーディレクトリ配下にリンクされた
// ワークツリーを並列で作成する。
//
// main はシグナル（Ctrl-C / SIGTERM）を context のキャンセルへ変換し、設定ファイルの
// 読み込みと環境変数の参照をここで 1 回だけ行い（cli.Parse は I/O を伴わない純関数）、
// App（ワークフローの束）を構築して Invocation を対応する型付きメソッドへディスパッチ
// する。メソッドが返す型付き Result は adapter/render で描画される（server status は
// --json で Result をそのまま JSON 出力できる）。
//
// 終了コード規約: 成功 = 0、エラー = 1、キャンセル（Ctrl-C / SIGTERM / 対話プロンプトの
// 中断）由来の失敗 = 130（シェルの 128+SIGINT 慣習に合わせる）。
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"syscall"

	"github.com/vimrak-hal/worktree-integrator/internal/adapter/cli"
	"github.com/vimrak-hal/worktree-integrator/internal/adapter/mcpserver"
	"github.com/vimrak-hal/worktree-integrator/internal/adapter/render"
	"github.com/vimrak-hal/worktree-integrator/internal/adapter/tui"
	"github.com/vimrak-hal/worktree-integrator/internal/app"
	"github.com/vimrak-hal/worktree-integrator/internal/app/action"
	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/childio"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/procctl"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/statedir"
)

func main() {
	// Ctrl-C / SIGTERM を ctx のキャンセルへ変換する。キャンセルは git・フック・
	// サーバーライフサイクルの子プロセスまで貫通する（デタッチ起動されたサーバーは
	// 仕様として対象外）。2 度目のシグナルは NotifyContext の解除後の既定動作で
	// 即時終了となる。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

// run はテスト可能なエントリポイント。ワークフローの出力（cobra のヘルプ／バージョン
// 含む）は stdout へ、エラーは stderr へ書き、終了コード（0 / 1 / 130）を返す。
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	inv, err := cli.Parse(args)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	// 実行モードのバリアントを先にさばく。HelpShown はテキストを書いて終わり、
	// MCP サーバーは JSON-RPC プロトコルのために stdio を占有するので直接ルーティング
	// する。ConfigCheck も App（Root・Proc 等）を必要としない独立した検証ロジックで
	// あるため、ここで処理する（どちらも設定ファイルの通常読み込みを経由しない）。
	switch v := inv.(type) {
	case cli.HelpShown:
		fmt.Fprint(stdout, v.Text)
		return 0
	case cli.RunMCP:
		if err := mcpserver.Serve(ctx); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return exitCode(ctx, err)
		}
		return 0
	case cli.ConfigCheck:
		return runConfigCheck(stdout, stderr)
	}

	// ワークフローの起動要求。設定ファイルと状態ルートはここで 1 回だけ解決し、
	// App（全ワークフロー共通の依存束）を構築する。
	file, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	root, err := statedir.Default()
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	// TUI は実行モードだが、設定と状態ルートを必要とするためここでさばく。App は
	// adapter/tui が TUI 専用の依存（子プロセス IO の切り離し・イベントの画面転送）で
	// 組み直すため、下の CLI 用 App は使わない。
	if _, ok := inv.(cli.RunUI); ok {
		if err := tui.Run(ctx, file, root); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return exitCode(ctx, err)
		}
		return 0
	}

	a := &app.App{
		Config:  file,
		Root:    root,
		ChildIO: childio.Inherit(),
		Proc:    procctl.NewUnixProcess(childio.Inherit()),
		// 非 TTY（パイプ・CI）では nil になり、対話選択を要する create は
		// 「--repo か --all を指定してください」エラーになる。
		Selector: cli.InteractiveSelector(),
		Progress: render.NewProgress(stdout),
	}

	if err := dispatch(ctx, a, inv, stdout); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitCode(ctx, err)
	}
	return 0
}

// dispatch は Invocation を対応する App メソッドへ振り分け、返った型付き Result を
// render で stdout へ描画する。エラー時も部分的な Result があれば描画してから
// エラーを返す（MCP アダプタと同じ「結果とエラーを別々に見せる」規約）。
// Invocation / ServerKind / AliasKind は封印された sum-type であり、App を必要と
// しないバリアント（HelpShown / RunMCP / ConfigCheck）は run が先にさばくため、
// 未知の値はバグでありパニックさせる。
func dispatch(ctx context.Context, a *app.App, inv cli.Invocation, stdout io.Writer) error {
	switch v := inv.(type) {
	case cli.Create:
		act, err := action.NewCreate(v.Name, v.Repos, v.All, v.Base, v.Ov, a.Config, os.Getenv, os.UserHomeDir)
		if err != nil {
			return err
		}
		res, err := a.Create(ctx, act)
		if res != nil {
			render.Create(stdout, res)
		}
		return err

	case cli.List:
		res, err := a.List(ctx)
		if err != nil {
			return err
		}
		if v.Json {
			return render.JSON(stdout, res)
		}
		render.List(stdout, res)
		return nil

	case cli.Enter:
		res, err := a.Enter(ctx, v.Name)
		if res != nil {
			render.Enter(stdout, res)
		}
		return err

	case cli.Remove:
		res, err := a.Remove(ctx, action.Remove{Name: v.Name, Force: v.Force, KeepBranch: v.KeepBranch})
		if res != nil {
			render.Remove(stdout, res)
		}
		return err

	case cli.Doctor:
		res, err := a.Doctor(ctx, v.Fix)
		if res != nil {
			if v.Json {
				if jsonErr := render.JSON(stdout, res); jsonErr != nil {
					return jsonErr
				}
			} else {
				render.Doctor(stdout, res)
			}
		}
		return err

	case cli.Repos:
		res, err := a.ListRepos(ctx)
		if err != nil {
			return err
		}
		render.Repos(stdout, res)
		return nil

	case cli.Server:
		cmd, err := action.NewServerCommand(v.Ov, a.Config, os.Getenv, os.UserHomeDir, v.Repos)
		if err != nil {
			return err
		}
		return dispatchServer(ctx, a, v, cmd, stdout)

	case cli.Alias:
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
			if err != nil {
				return err
			}
			render.Aliases(stdout, res)
			return nil
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

	default:
		panic(fmt.Sprintf("unknown cli.Invocation %T", inv))
	}
}

// dispatchServer は server の操作を対応する App メソッドへ振り分ける。
func dispatchServer(ctx context.Context, a *app.App, v cli.Server, cmd action.ServerCommand, stdout io.Writer) error {
	switch k := v.Kind.(type) {
	case action.SwitchKind:
		res, err := a.ServerSwitch(ctx, cmd, k)
		if res != nil {
			render.Switch(stdout, res)
		}
		return err

	case action.StatusKind:
		res, err := a.ServerStatus(ctx, cmd)
		if err != nil {
			return err
		}
		if v.Json {
			return render.JSON(stdout, res)
		}
		render.Status(stdout, res)
		return nil

	case action.StopKind:
		res, err := a.ServerStop(ctx, cmd, k)
		if res != nil {
			render.Stop(stdout, res)
		}
		return err

	case action.LogsKind:
		res, err := a.ServerLogs(ctx, cmd, k)
		if err != nil {
			return err
		}
		// -f（tail -f）は CLI 専用の表示手段: ワークフローが解決したログパスを
		// 受けて自前で tail を実行する。存在しないログは追跡できないため除く。
		if v.FollowLogs {
			var paths []string
			for _, entry := range res.Logs {
				if entry.Missing {
					fmt.Fprintf(stdout, "  [%s/%s] ログがありません (%s)\n", entry.Repo, entry.Server, entry.Path)
					continue
				}
				fmt.Fprintf(stdout, "  [%s/%s] %s\n", entry.Repo, entry.Server, entry.Path)
				paths = append(paths, entry.Path)
			}
			if len(paths) == 0 {
				fmt.Fprintln(stdout, "表示できるログがありません")
				return nil
			}
			return cli.FollowLogs(ctx, paths, k.Lines)
		}
		render.Logs(stdout, res)
		return nil

	default:
		panic(fmt.Sprintf("unknown action.ServerKind %T", v.Kind))
	}
}

// exitCode は失敗を終了コードへ写像する。キャンセル由来 — エラー自体が
// context.Canceled を包んでいる、対話プロンプトが中断された（cli.ErrInterrupted）、
// またはシグナルで ctx がキャンセル済み — は 130、それ以外は 1。最後の条件は、
// キャンセルが Outcome の集約などでエラー型として残らない経路（例: worktree 作成の
// 部分失敗サマリ）でも 130 を保証する。
func exitCode(ctx context.Context, err error) int {
	if errors.Is(err, context.Canceled) || errors.Is(err, cli.ErrInterrupted) || ctx.Err() != nil {
		return 130
	}
	return 1
}

// runConfigCheck は `wt config check` を実装する。設定ファイルを既定パスから読み込み、
// 全 Validate を通す（LoadFrom が「デコード → 各所有パッケージの Validate 呼び出し」を
// 既に行っている）。ワークフロー起動要求が経由する App / config.Load の経路（ファイル
// 不存在を静かに空扱いする）は意図的に使わない: config check は次の 3 通りを区別して
// 報告する必要があるためである。
//
//   - ファイル不存在: 「設定ファイルがありません（<path>）。既定値で動作します」/ exit 0
//   - 存在し正常: 「設定は正常です（<path>）」/ exit 0
//   - 存在するが不正: 検証エラーを stderr へ / exit 1
func runConfigCheck(stdout, stderr io.Writer) int {
	path, ok := config.DefaultPath()
	if !ok {
		fmt.Fprintln(stderr, "error: 設定ファイルの既定パスを解決できません（ホームディレクトリを特定できません）")
		return 1
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(stdout, "設定ファイルがありません（%s）。既定値で動作します\n", path)
		return 0
	} else if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if _, err := config.LoadFrom(path); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "設定は正常です（%s）\n", path)
	return 0
}
