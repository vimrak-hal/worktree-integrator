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
// App（ワークフローの束）を構築して adapter/clirun へ配線する（Invocation の振り分けと
// 型付き Result の描画は clirun が担う）。main 自体は dispatch や整形のロジックを持たず、
// 「解析 → App 構築 → 振り分け → エラーの終了コードへの写像」の配線に徹する。
//
// 終了コード規約: 成功 = 0、エラー = 1、キャンセル（Ctrl-C / SIGTERM / 対話プロンプトの
// 中断）由来の失敗 = 130（シェルの 128+SIGINT 慣習に合わせる）。
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/vimrak-hal/worktree-integrator/internal/adapter/cli"
	"github.com/vimrak-hal/worktree-integrator/internal/adapter/clirun"
	"github.com/vimrak-hal/worktree-integrator/internal/adapter/mcpserver"
	"github.com/vimrak-hal/worktree-integrator/internal/adapter/render"
	"github.com/vimrak-hal/worktree-integrator/internal/adapter/tui"
	"github.com/vimrak-hal/worktree-integrator/internal/app"
	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/childio"
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
	// する。ConfigCheck も App（Root・Proc 等）を必要とせず、かつ設定ファイルの通常
	// 読み込み（不存在を静かに空扱いする経路）を意図的に避けるため、ここで clirun へ
	// 配線する（不正な設定ファイルはエラーとして返り、下の config.Load では潰せない）。
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
		if err := clirun.ConfigCheck(stdout); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		return 0
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

	a := app.New(file, root, childio.Inherit(),
		// 非 TTY（パイプ・CI）では nil になり、対話選択を要する create は
		// 「--repo か --all を指定してください」エラーになる。
		app.WithSelector(cli.InteractiveSelector()),
		app.WithProgress(render.NewProgress(stdout)),
	)

	if err := clirun.Run(ctx, inv, a, stdout); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitCode(ctx, err)
	}
	return 0
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
