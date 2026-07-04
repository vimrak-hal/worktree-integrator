package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vimrak-hal/worktree-integrator/internal/app"
)

// 旧実装の finish() / "(完了)" プレースホルダの契約テストは、typed Result +
// structuredContent への移行（意図的な仕様変更）に伴い破棄された。ツール結果の
// 契約は TestHandleWrapsActionResult と structured_test.go が固定する。

// TestIsCleanShutdown は、クライアント切断の通常経路（EOF・接続クローズ・自前で
// 観測した stdin の EOF）だけがクリーンと判定され、それ以外のエラーは伝播対象として
// 扱われることを固定する。SDK 内部のエラーメッセージ（例: "server is closing"）への
// 文字列照合は廃止されており、stdin の EOF を観測していない限りクリーンにならない。
func TestIsCleanShutdown(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		stdinEOF bool
		want     bool
	}{
		{"io.EOF はクリーン", io.EOF, false, true},
		{"ラップされた io.EOF もクリーン", fmt.Errorf("read: %w", io.EOF), false, true},
		{"接続クローズはクリーン", mcp.ErrConnectionClosed, false, true},
		{"ラップされた接続クローズもクリーン", fmt.Errorf("rpc: %w", mcp.ErrConnectionClosed), false, true},
		{"stdin EOF 観測後は SDK 内部エラーもクリーン", errors.New("jsonrpc2: server is closing"), true, true},
		{"stdin EOF 未観測なら SDK 内部の文言ではクリーンにしない", errors.New("jsonrpc2: server is closing"), false, false},
		{"無関係なエラーはクリーンでない", errors.New("boom"), false, false},
		{"無関係なエラーも stdin EOF 観測後はクリーン", errors.New("boom"), true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isCleanShutdown(tt.err, tt.stdinEOF); got != tt.want {
				t.Fatalf("isCleanShutdown(%v, %v) = %v, want %v", tt.err, tt.stdinEOF, got, tt.want)
			}
		})
	}
}

// TestEOFTrackingReader は、ラップした reader が EOF への到達を記録することを固定する。
func TestEOFTrackingReader(t *testing.T) {
	r := &eofTrackingReader{inner: io.NopCloser(strings.NewReader("x"))}
	buf := make([]byte, 4)
	if _, err := r.Read(buf); err != nil {
		t.Fatalf("first read: %v", err)
	}
	if r.sawEOF() {
		t.Fatal("EOF must not be recorded before it is reached")
	}
	if _, err := r.Read(buf); !errors.Is(err, io.EOF) {
		t.Fatalf("second read = %v, want io.EOF", err)
	}
	if !r.sawEOF() {
		t.Fatal("EOF should be recorded after it is reached")
	}
}

// isolate は HOME（設定ファイルの探索先）と XDG_STATE_HOME（別名・サーバー状態の
// 保存先）を一時ディレクトリへ向け、実 HOME を汚さずにアクションを丸ごと走らせられる
// ようにする。設定ファイルは置かないため、[servers.*] 未設定・別名なしの初期状態となる。
// （cli.Parse と異なり、MCP のアクションは実際に config.Load / os.Getenv を使うため、
// この隔離は引き続き必要である。）
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("WT_REPOS_DIR", "")
	t.Setenv("WT_WORKTREES_DIR", "")
	t.Setenv("WT_REMOTE", "")
	t.Setenv("WT_CONCURRENCY", "")
}

// TestAliasActionsRoundTrip は、別名アクション群を MCP のパラメータから丸ごと
// 駆動し、set → list → remove の状態遷移が構造化結果とテキストの両方に正しく
// 反映されることを検証する。
func TestAliasActionsRoundTrip(t *testing.T) {
	isolate(t)

	// 別名が未登録の初期状態。構造化結果は空マップ（null ではない）。
	res, out, err := actionAliasList(t.Context(), NoParams{})
	if err != nil || !strings.Contains(out, "登録されていません") {
		t.Fatalf("初期 list = %q, %v", out, err)
	}
	if res == nil || res.Aliases == nil || len(res.Aliases) != 0 {
		t.Fatalf("初期 aliases = %#v", res)
	}

	// 設定すると、設定された名前と値がテキストに表れる。
	if out, err := actionAliasSet(t.Context(), AliasSetParams{WorktreeName: "feat-x", Alias: "Login"}); err != nil ||
		!strings.Contains(out, "feat-x") || !strings.Contains(out, "Login") {
		t.Fatalf("set = %q, %v", out, err)
	}
	// list は構造化結果とテキストの両方に名前とラベルを含む。
	res, out, err = actionAliasList(t.Context(), NoParams{})
	if err != nil || !strings.Contains(out, "feat-x") || !strings.Contains(out, "Login") {
		t.Fatalf("設定後 list = %q, %v", out, err)
	}
	if res.Aliases["feat-x"] != "Login" {
		t.Fatalf("aliases = %+v", res.Aliases)
	}

	// 削除すると除去され、再度の list は空に戻る。
	if out, err := actionAliasRemove(t.Context(), AliasNameParams{WorktreeName: "feat-x"}); err != nil ||
		!strings.Contains(out, "feat-x") {
		t.Fatalf("remove = %q, %v", out, err)
	}
	if res, out, err := actionAliasList(t.Context(), NoParams{}); err != nil ||
		!strings.Contains(out, "登録されていません") || len(res.Aliases) != 0 {
		t.Fatalf("削除後 list = %q, %+v, %v", out, res, err)
	}
}

// TestAliasSetEmptyIsError は、空の alias がエラーになり、既存の別名を消さないことを
// 固定する（削除は alias_remove の 1 経路のみ）。
func TestAliasSetEmptyIsError(t *testing.T) {
	isolate(t)

	if _, err := actionAliasSet(t.Context(), AliasSetParams{WorktreeName: "feat-x", Alias: "Login"}); err != nil {
		t.Fatalf("事前 set: %v", err)
	}
	if _, err := actionAliasSet(t.Context(), AliasSetParams{WorktreeName: "feat-x", Alias: ""}); err == nil {
		t.Fatal("空 alias の set はエラーになるべき")
	}
	// 既存の別名は保持され、list に残る。
	if res, _, err := actionAliasList(t.Context(), NoParams{}); err != nil || res.Aliases["feat-x"] != "Login" {
		t.Fatalf("list = %+v, %v", res, err)
	}
}

// TestAliasActionsRejectInvalidName は、不正な worktree 名が ParseName の検証で
// 弾かれ、各アクションがエラーを返すことを固定する（set / remove は名前を検証する。
// list は検証しない）。
func TestAliasActionsRejectInvalidName(t *testing.T) {
	isolate(t)

	const bad = "bad name!" // 空白と記号を含むため不正
	if _, err := actionAliasSet(t.Context(), AliasSetParams{WorktreeName: bad, Alias: "x"}); err == nil {
		t.Fatal("不正名の set はエラーになるべき")
	}
	if _, err := actionAliasRemove(t.Context(), AliasNameParams{WorktreeName: bad}); err == nil {
		t.Fatal("不正名の remove はエラーになるべき")
	}
}

// TestServerActionsWithoutConfig は、設定ファイルなし（[servers.*] 未設定）の状態で
// status / stop / logs アクションを駆動し、いずれもエラーにならず、それぞれの
// 「何もない」案内と構造化結果を返すことを検証する。App → ワークフロー → render の
// 経路を、子プロセスを起動しない安全な範囲で貫通する。
func TestServerActionsWithoutConfig(t *testing.T) {
	isolate(t)

	t.Run("status は [servers.*] 未設定を案内", func(t *testing.T) {
		res, out, err := actionServerStatus(t.Context(), ServerScopeParams{})
		if err != nil || !strings.Contains(out, "サーバー設定がありません") {
			t.Fatalf("status = %q, %v", out, err)
		}
		if res == nil || !res.NoServerConfig || len(res.Rows) != 0 {
			t.Fatalf("res = %+v", res)
		}
	})

	t.Run("stop は worktree 名省略で全件対象", func(t *testing.T) {
		res, out, err := actionServerStop(t.Context(), ServerStopParams{})
		if err != nil || !strings.Contains(out, "停止対象のサーバーはありません") {
			t.Fatalf("stop = %q, %v", out, err)
		}
		if res == nil || res.Stopped != 0 {
			t.Fatalf("res = %+v", res)
		}
	})

	t.Run("logs は既定 50 行で worktree 名省略", func(t *testing.T) {
		res, out, err := actionServerLogs(t.Context(), ServerLogsParams{})
		if err != nil || !strings.Contains(out, "表示できるログがありません") {
			t.Fatalf("logs = %q, %v", out, err)
		}
		if res == nil || len(res.Logs) != 0 {
			t.Fatalf("res = %+v", res)
		}
	})
}

// textsOf は CallToolResult の TextContent 群を取り出す。
func textsOf(t *testing.T, res *mcp.CallToolResult) []string {
	t.Helper()
	var out []string
	for _, c := range res.Content {
		tc, ok := c.(*mcp.TextContent)
		if !ok {
			t.Fatalf("Content は *TextContent であるべき, got %T", c)
		}
		out = append(out, tc.Text)
	}
	return out
}

// TestHandleWrapsActionResult は、handle がアクションの (result, text, err) を MCP の
// ツール結果へ写す契約を固定する。
func TestHandleWrapsActionResult(t *testing.T) {
	type out struct {
		Value string `json:"value"`
	}

	t.Run("成功はテキストと Result を返し IsError を立てない", func(t *testing.T) {
		h := handle(func(context.Context, NoParams) (*out, string, error) {
			return &out{Value: "v"}, "ok-body", nil
		})
		res, structured, err := h(context.Background(), nil, NoParams{})
		if err != nil {
			t.Fatalf("プロトコルエラーは返らないべき: %v", err)
		}
		if res.IsError {
			t.Fatal("成功時に IsError を立ててはならない")
		}
		if texts := textsOf(t, res); len(texts) != 1 || texts[0] != "ok-body" {
			t.Fatalf("本文 = %q", texts)
		}
		if structured == nil || structured.Value != "v" {
			t.Fatalf("structured = %+v", structured)
		}
	})

	t.Run("結果もテキストも無いエラーは SDK に畳み込ませる", func(t *testing.T) {
		h := handle(func(context.Context, NoParams) (*out, string, error) {
			return nil, "", errors.New("boom")
		})
		res, structured, err := h(context.Background(), nil, NoParams{})
		if err == nil || err.Error() != "boom" {
			t.Fatalf("err = %v", err)
		}
		if res != nil || structured != nil {
			t.Fatalf("res = %+v, structured = %+v", res, structured)
		}
	})

	t.Run("途中まで進んだエラーはテキストとエラーを別々の TextContent で返す", func(t *testing.T) {
		h := handle(func(context.Context, NoParams) (*out, string, error) {
			return &out{Value: "partial"}, "partial progress\n", errors.New("boom")
		})
		res, structured, err := h(context.Background(), nil, NoParams{})
		if err != nil {
			t.Fatalf("プロトコルエラーは返らないべき: %v", err)
		}
		if !res.IsError {
			t.Fatal("エラー時は IsError を立てるべき")
		}
		texts := textsOf(t, res)
		if len(texts) != 2 {
			t.Fatalf("テキストは（進捗・エラー）の 2 件であるべき: %q", texts)
		}
		if !strings.Contains(texts[0], "partial progress") {
			t.Fatalf("進捗テキスト = %q", texts[0])
		}
		if !strings.Contains(texts[1], "エラー: boom") {
			t.Fatalf("エラーテキスト = %q", texts[1])
		}
		// 部分 Result も structuredContent として返る。
		if structured == nil || structured.Value != "partial" {
			t.Fatalf("structured = %+v", structured)
		}
	})
}

// TestActionCreateWorktreesRejectsEmptyRepos は、repos が空のとき create が設定の
// 読み込みや worktree 作成へ進む前に、案内付きのエラーで早期に弾くことを固定する。
func TestActionCreateWorktreesRejectsEmptyRepos(t *testing.T) {
	isolate(t)

	_, _, err := actionCreateWorktrees(t.Context(), CreateParams{WorktreeName: "feat-x", Repos: nil})
	if err == nil {
		t.Fatal("空の repos はエラーになるべき")
	}
	if !strings.Contains(err.Error(), "repos_list") {
		t.Fatalf("エラーは repos_list の案内を含むべき, got %q", err)
	}
}

// TestActionCreateWorktreesRejectsUnknownRepo は、存在しないリポジトリ名がワーク
// フロー内の照合（app/create）でエラーになることを固定する。CLI の --repo と同じ
// 1 箇所の検証を通る。
func TestActionCreateWorktreesRejectsUnknownRepo(t *testing.T) {
	isolate(t)
	t.Setenv("WT_REPOS_DIR", t.TempDir()) // 空の repos_dir
	t.Setenv("WT_WORKTREES_DIR", t.TempDir())

	_, _, err := actionCreateWorktrees(t.Context(), CreateParams{WorktreeName: "feat-x", Repos: []string{"nope"}})
	if err == nil {
		t.Fatal("未知のリポジトリ名はエラーになるべき")
	}
	if !strings.Contains(err.Error(), "nope") || !strings.Contains(err.Error(), "見つかりません") {
		t.Fatalf("err = %q", err)
	}
}

// TestActionReposList は、repos_list が構造化結果（repos_dir と一覧）を返すことを
// 固定する。
func TestActionReposList(t *testing.T) {
	isolate(t)
	reposDir := t.TempDir()
	t.Setenv("WT_REPOS_DIR", reposDir)

	res, out, err := actionReposList(t.Context(), NoParams{})
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || res.ReposDir != reposDir {
		t.Fatalf("res = %+v", res)
	}
	if res.Repos == nil || len(res.Repos) != 0 {
		t.Fatalf("repos は非 nil の空スライスであるべき: %#v", res.Repos)
	}
	if !strings.Contains(out, "リポジトリが見つかりません") {
		t.Fatalf("out = %q", out)
	}
}

// TestActionServerLogsHonorsLines は、Lines を明示したときにクランプ後の値が採用
// される経路（0 なら既定 50）を通すための補完。設定なしでも targets が空なので案内に
// 落ちるが、既定 50 とは別の分岐を踏むことに意味がある。
func TestActionServerLogsHonorsLines(t *testing.T) {
	isolate(t)

	_, out, err := actionServerLogs(t.Context(), ServerLogsParams{Lines: 5})
	if err != nil || !strings.Contains(out, "表示できるログがありません") {
		t.Fatalf("Lines 指定 logs = %q, %v", out, err)
	}
}

// TestServerActionsValidateWorktreeName は、stop / logs が worktree 名を絞り込み
// (OneWorktree) に変換する際、その名前が検証され、不正・空文字列なら
// エラーになることを固定する（省略のみが全件対象）。
func TestServerActionsValidateWorktreeName(t *testing.T) {
	isolate(t)

	bad := "bad name!"
	if _, _, err := actionServerStop(t.Context(), ServerStopParams{WorktreeName: &bad}); err == nil {
		t.Fatal("不正な worktree 名の stop はエラーになるべき")
	}
	if _, _, err := actionServerLogs(t.Context(), ServerLogsParams{WorktreeName: &bad}); err == nil {
		t.Fatal("不正な worktree 名の logs はエラーになるべき")
	}
	empty := ""
	if _, _, err := actionServerStop(t.Context(), ServerStopParams{WorktreeName: &empty}); err == nil {
		t.Fatal("空文字列の worktree 名の stop はエラーになるべき（全件は省略のみ）")
	}
	if _, _, err := actionServerLogs(t.Context(), ServerLogsParams{WorktreeName: &empty}); err == nil {
		t.Fatal("空文字列の worktree 名の logs はエラーになるべき（全件は省略のみ）")
	}
}

// コンパイル時の型リンク: actionAliasList の Result は app.AliasesResult である
// （構造化スキーマの型が App 層の Result と同一物であることの静的な固定）。
var _ func(context.Context, NoParams) (*app.AliasesResult, string, error) = actionAliasList
