// Package mcpserver はツールのワークフローを stdio 経由で MCP（Model Context Protocol）
// サーバーとして公開し、MCP クライアントが CLI と同じように worktree の作成や
// 開発サーバー / エイリアスの操作を行えるようにする。
//
// このサーバーは再実装ではなく薄いアダプターである。各ツールは CLI の main と同じ
// App の型付きメソッドを呼び、返った型付き Result を mcp.ToolHandlerFor の Out として
// そのまま返す — SDK が Result 型から出力スキーマを自動生成し、structuredContent と
// して直列化する。人間向けのテキストは同じ Result を adapter/render で取り込み
// バッファに描画したもので、TextContent として併記される。エラー時は取り込み済みの
// テキストとエラーを別々の TextContent で返す（旧実装の finish() による「進捗を
// エラー文字列へ織り込む」方式と "(完了)" プレースホルダは全廃した）。
//
// stdio の衛生管理: JSON-RPC プロトコルはこのプロセスの stdin/stdout を流れる。
// ワークフローの描画は取り込み用バッファにのみ書き込まれ、そこから起動される
// 子プロセス（フックやサーバーのライフサイクルコマンド）には childio.Quiet を介して
// 明示的な標準ストリーム（stdin = /dev/null、出力 = stderr）が与えられる。これにより
// どの子プロセスも stdin からプロトコルのバイトを読み取ることも、プロトコルストリームに
// 書き込むこともできない。デタッチされたサーバーの起動は既にログファイルへ
// リダイレクトされている。`server logs` のフォロー（tail -f）は action の語彙に
// 存在せず、MCP からは型レベルで到達不能である。
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vimrak-hal/worktree-integrator/internal/buildinfo"
)

const instructions = "Create Git worktrees across repositories and manage their dev servers. " +
	"Discovery flow: call repos_list first to discover repository names, then worktree_create " +
	"with an explicit `repos` list (unknown names are an error), then server_switch to run that " +
	"worktree's dev servers. worktree_list shows every existing worktree with its alias, member " +
	"repositories and running servers. worktree_remove is DESTRUCTIVE: it stops the worktree's " +
	"servers, deletes the checkouts and the branch (unless keep_branch), and cleans up state, " +
	"alias and logs; checkouts with uncommitted changes are refused (there is no force option). " +
	"Manage servers with server_switch/status/stop/logs and labels with alias_set/list/remove. " +
	"Directories come from the configuration file only; there are no per-call directory " +
	"overrides. Every listing tool returns structuredContent mirroring the human-readable text."

// newServer は全ツールを登録した MCP サーバーを構築する（Serve とテストが共用する）。
func newServer() *mcp.Server {
	srv := mcp.NewServer(
		&mcp.Implementation{Name: "worktree-integrator", Version: buildinfo.Version()},
		&mcp.ServerOptions{Instructions: instructions},
	)

	// ツール名は noun_verb で統一されている（旧 list_repos / create_worktrees から
	// 改名 — 意図的な仕様変更。クライアントは再接続時に新しい名前を取得する）。
	mcp.AddTool(srv, &mcp.Tool{
		Name: "repos_list",
		Description: "List the Git repositories under the repositories directory. " +
			"These names are the candidates for worktree_create's `repos`.",
	}, handle(actionReposList))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "worktree_create",
		Description: "Create a Git worktree named `worktree_name` (also the branch name) in each of " +
			"the explicitly listed `repos`, fetching the latest commit of the base branch (auto-detected " +
			"remote default, or overridden by `base` / repos.<repo>.base / defaults.base), copying " +
			"configured extra files and running any configured hooks. Unknown repository names are an " +
			"error; use repos_list to discover them.",
	}, handle(actionCreateWorktrees))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "worktree_list",
		Description: "List every worktree with its alias, member repository checkouts (and their " +
			"branches/health) and currently running servers. Broken checkouts (dead gitdir " +
			"pointers, e.g. after a manual rm -rf of the source repository) are flagged.",
	}, handle(actionWorktreeList))

	// force に相当するパラメータは意図的に存在しない: dirty なチェックアウトの削除は
	// エラーとして返るのみで、強制削除は CLI（remove --force）専用である。
	destructive := true
	mcp.AddTool(srv, &mcp.Tool{
		Name: "worktree_remove",
		Description: "DESTRUCTIVE: remove the worktree named `name` completely — stop its running " +
			"dev servers, `git worktree remove` each checkout (refused if a checkout has " +
			"uncommitted changes; there is no force), delete the branch (unless `keep_branch`), " +
			"and clean up setup records, the alias and log files.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &destructive},
	}, handle(actionWorktreeRemove))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "server_switch",
		Description: "Switch the dev server(s) of the matching repositories to `worktree_name`, " +
			"stopping each server's previous instance. Empty `repos` targets every " +
			"configured repository whose worktree exists.",
	}, handle(actionServerSwitch))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "server_status",
		Description: "Show, per repository and server, which worktree each server is running for, " +
			"its alias, the server state (running/stopped/crashed) and PID.",
	}, handle(actionServerStatus))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "server_stop",
		Description: "Stop running dev servers. Omit `worktree_name` to stop all; otherwise only " +
			"the servers running for that worktree (an empty string is an error). " +
			"Empty `repos` targets every repository.",
	}, handle(actionServerStop))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "server_logs",
		// 既定行数・上限は定数から文言を生成し、jsonschema タグ・clampLines との三重の
		// 手書きを 1 つ（定数）に寄せる（params.go の serverLogsLineLimit / defaultLogLines）。
		Description: fmt.Sprintf("Read the trailing `lines` (default %d, at most %d) of dev-server logs. With a "+
			"`worktree_name`, read that worktree's logs (running or not); otherwise the currently "+
			"running servers' (falling back to the last-started log when a server is down). "+
			"Set `prev` to read the previous log generation rotated aside at the last server start.",
			defaultLogLines, serverLogsLineLimit),
	}, handle(actionServerLogs))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "alias_set",
		Description: "Set (or update) the display alias for a worktree, shown in server_status. " +
			"An empty `alias` is an error; use alias_remove to clear.",
	}, handleText(actionAliasSet))

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "alias_list",
		Description: "List every worktree display alias.",
	}, handle(actionAliasList))

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "alias_remove",
		Description: "Remove a worktree's display alias.",
	}, handleText(actionAliasRemove))

	return srv
}

// Serve はクライアントが切断するか ctx がキャンセルされるまで、MCP サーバーを
// stdio 経由で実行する。
func Serve(ctx context.Context) error {
	srv := newServer()

	// stdio サーバーの通常のライフサイクルはクライアントが接続を閉じたとき
	// （stdin の EOF）に終わる。EOF の観測は SDK のエラー文言に頼らず、stdin を
	// ラップして自前で記録する。そのクリーンなシャットダウンはエラーではなく
	// 成功として扱う。
	stdin := &eofTrackingReader{inner: os.Stdin}
	transport := &mcp.IOTransport{Reader: stdin, Writer: nopWriteCloser{os.Stdout}}
	if err := srv.Run(ctx, transport); err != nil && !isCleanShutdown(err, stdin.sawEOF()) {
		return err
	}
	return nil
}

// eofTrackingReader は stdin をラップし、EOF への到達を記録する。SDK が EOF 起因の
// シャットダウンを内部エラー（例: jsonrpc2 の "server is closing"）に変換して返す
// 場合があり、そのメッセージ文字列への照合は SDK の内部実装への依存になる。代わりに
// 「stdin が EOF に達した後のエラーはクリーンシャットダウン」という自前の事実で
// 判定する。
type eofTrackingReader struct {
	inner io.ReadCloser
	eof   atomic.Bool
}

func (r *eofTrackingReader) Read(p []byte) (int, error) {
	n, err := r.inner.Read(p)
	if errors.Is(err, io.EOF) {
		r.eof.Store(true)
	}
	return n, err
}

func (r *eofTrackingReader) Close() error { return r.inner.Close() }

// sawEOF は stdin が EOF に達したかどうかを報告する。
func (r *eofTrackingReader) sawEOF() bool { return r.eof.Load() }

// nopWriteCloser は stdout を IOTransport の要求する io.WriteCloser に適合させる。
// プロセスの stdout をトランスポートの都合でクローズしてはならないため、Close は
// 何もしない（SDK 自身の StdioTransport と同じ扱い）。
type nopWriteCloser struct {
	io.Writer
}

func (nopWriteCloser) Close() error { return nil }

// isCleanShutdown は err が単なるクライアントの切断（stdin での EOF や接続のクローズ）か
// どうかを返す。これは stdio サーバーが終了する通常の経路である。stdinEOF は自前で
// 観測した「stdin が EOF に達した」という事実で、EOF 起因のシャットダウンと競合した
// リクエストのエラーも SDK の文言に依存せずクリーンと判定できる。
func isCleanShutdown(err error, stdinEOF bool) bool {
	return errors.Is(err, io.EOF) ||
		errors.Is(err, mcp.ErrConnectionClosed) ||
		stdinEOF
}

// handle は「型付き Result・人間向けテキスト・エラー」を返すアクションを、
// structuredContent 付きの MCP ツールハンドラーに適合させる。SDK は Out 型
// （*Out の要素型）から出力スキーマを自動生成し、返した Result を検証のうえ
// structuredContent として直列化する。SDK から渡される ctx はアクションへ
// そのまま伝播し、クライアントのキャンセルがワークフローと子プロセスに届く。
//
// エラーの扱い: 途中まで進んだ結果（テキストまたは部分 Result）があれば、
// IsError のツール結果に「取り込んだテキスト」と「エラー」を別々の TextContent と
// して積む。何も無ければ SDK にエラーをそのまま畳み込ませる。いずれもプロトコル
// エラーにはならない。
func handle[In, Out any](action func(context.Context, In) (*Out, string, error)) mcp.ToolHandlerFor[In, *Out] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, *Out, error) {
		out, text, err := action(ctx, in)
		if err != nil {
			if out == nil && strings.TrimSpace(text) == "" {
				return nil, nil, err
			}
			var content []mcp.Content
			if strings.TrimSpace(text) != "" {
				content = append(content, &mcp.TextContent{Text: text})
			}
			content = append(content, &mcp.TextContent{Text: "エラー: " + err.Error()})
			return &mcp.CallToolResult{IsError: true, Content: content}, out, nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, out, nil
	}
}

// handleText はテキストのみを返すアクション（alias_set / alias_remove のような、
// 構造化すべき結果を持たない操作）を MCP ツールハンドラーに適合させる。Out が any の
// ため出力スキーマは生成されず、structuredContent は付かない。
func handleText[In any](action func(context.Context, In) (string, error)) mcp.ToolHandlerFor[In, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		text, err := action(ctx, in)
		if err != nil {
			return nil, nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
	}
}
