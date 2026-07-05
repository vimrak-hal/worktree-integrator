package mcpserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vimrak-hal/worktree-integrator/internal/infra/testutil"
)

// connect はインメモリのトランスポートで newServer に接続したクライアント
// セッションを返す。structuredContent と出力スキーマを、実際のプロトコル往復
// （JSON 直列化を含む）を通して検証するためのハーネスである。
func connect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ct, st := mcp.NewInMemoryTransports()
	srv := newServer()
	serverSession, err := srv.Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := client.Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// structuredOf は structuredContent を map へ復元する（ワイヤ上では JSON
// オブジェクト）。
func structuredOf(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	if res.StructuredContent == nil {
		t.Fatalf("structuredContent が無い: %+v", res)
	}
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("structuredContent はオブジェクトであるべき: %v", err)
	}
	return m
}

// ツール一覧: noun_verb へ統一された 11 ツールが公開され、列挙系ツールには出力
// スキーマ（structuredContent の契約）が付く。alias_set / alias_remove は
// テキストのみ（出力スキーマなし）。
func TestToolCatalogAndOutputSchemas(t *testing.T) {
	isolate(t)
	cs := connect(t)

	list, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*mcp.Tool{}
	var names []string
	for _, tool := range list.Tools {
		byName[tool.Name] = tool
		names = append(names, tool.Name)
	}
	want := []string{
		"repos_list", "worktree_create", "worktree_list", "worktree_remove",
		"server_switch", "server_status", "server_stop", "server_logs",
		"alias_set", "alias_list", "alias_remove",
	}
	for _, name := range want {
		if byName[name] == nil {
			t.Errorf("tool %q missing (have %v)", name, names)
		}
	}
	if len(list.Tools) != len(want) {
		t.Errorf("tools = %v, want exactly %d", names, len(want))
	}
	// 旧名は存在しない。
	for _, old := range []string{"list_repos", "create_worktrees", "alias_get"} {
		if byName[old] != nil {
			t.Errorf("old tool name %q must be gone", old)
		}
	}
	// 列挙系には出力スキーマが付き、テキストのみの alias_set / alias_remove には
	// 付かない。
	for _, name := range []string{
		"repos_list", "worktree_create", "worktree_list", "worktree_remove",
		"server_switch", "server_status", "server_stop", "server_logs", "alias_list",
	} {
		if byName[name].OutputSchema == nil {
			t.Errorf("%s should have an output schema", name)
		}
	}
	for _, name := range []string{"alias_set", "alias_remove"} {
		if byName[name].OutputSchema != nil {
			t.Errorf("%s should not have an output schema", name)
		}
	}
	// worktree_remove は破壊的操作としてアノテートされ、`force` パラメータは公開
	// されない（dirty はエラーを返すのみ — 強制削除は CLI 専用）。
	remove := byName["worktree_remove"]
	if remove.Annotations == nil || remove.Annotations.DestructiveHint == nil || !*remove.Annotations.DestructiveHint {
		t.Error("worktree_remove should carry DestructiveHint=true")
	}
	if schema, err := json.Marshal(remove.InputSchema); err != nil || strings.Contains(string(schema), "force") {
		t.Errorf("worktree_remove must not expose a force parameter: %s (err=%v)", schema, err)
	}
}

// alias_list の structuredContent は {aliases: {name: label}} の JSON 形になり、
// 人間向けのテキストが併記される。
func TestAliasListStructuredContent(t *testing.T) {
	isolate(t)
	cs := connect(t)

	set, err := cs.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "alias_set",
		Arguments: map[string]any{"worktree_name": "feat-x", "alias": "Login"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if set.IsError {
		t.Fatalf("alias_set failed: %+v", set.Content)
	}
	// alias_set はテキストのみで structuredContent を持たない。
	if set.StructuredContent != nil {
		t.Fatalf("alias_set must not return structuredContent: %+v", set.StructuredContent)
	}

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "alias_list"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("alias_list failed: %+v", res.Content)
	}
	structured := structuredOf(t, res)
	aliases, ok := structured["aliases"].(map[string]any)
	if !ok || aliases["feat-x"] != "Login" {
		t.Fatalf("structured = %+v", structured)
	}
	// 人間向けテキストの併記。
	if len(res.Content) == 0 {
		t.Fatal("human-readable text content missing")
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "feat-x") || !strings.Contains(text, "Login") {
		t.Fatalf("text = %q", text)
	}
}

// server_status の structuredContent は StatusResult の JSON 形（no_server_config /
// rows）になる。
func TestServerStatusStructuredContent(t *testing.T) {
	isolate(t)
	cs := connect(t)

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "server_status"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("server_status failed: %+v", res.Content)
	}
	structured := structuredOf(t, res)
	if structured["no_server_config"] != true {
		t.Fatalf("structured = %+v", structured)
	}
	if _, present := structured["rows"]; present {
		// omitempty のため空の rows は現れない（現れる場合は配列であること）。
		if _, ok := structured["rows"].([]any); !ok {
			t.Fatalf("rows should be an array: %+v", structured["rows"])
		}
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "サーバー設定がありません") {
		t.Fatalf("text = %q", text)
	}
}

// repos_list の structuredContent は {repos_dir, repos: [{name, path}]} の JSON 形になる。
func TestReposListStructuredContent(t *testing.T) {
	isolate(t)
	reposDir := t.TempDir()
	t.Setenv("WT_REPOS_DIR", reposDir)
	cs := connect(t)

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "repos_list"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("repos_list failed: %+v", res.Content)
	}
	structured := structuredOf(t, res)
	if structured["repos_dir"] != reposDir {
		t.Fatalf("structured = %+v", structured)
	}
	repos, ok := structured["repos"].([]any)
	if !ok || len(repos) != 0 {
		t.Fatalf("repos = %#v", structured["repos"])
	}
}

// worktree_list の structuredContent は {worktrees_dir, worktrees: []} の JSON 形になる
// （空でも null にならない）。
func TestWorktreeListStructuredContent(t *testing.T) {
	isolate(t)
	reposDir := t.TempDir()
	worktreesDir := t.TempDir()
	t.Setenv("WT_REPOS_DIR", reposDir)
	t.Setenv("WT_WORKTREES_DIR", worktreesDir)
	cs := connect(t)

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "worktree_list"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("worktree_list failed: %+v", res.Content)
	}
	structured := structuredOf(t, res)
	if structured["worktrees_dir"] != worktreesDir {
		t.Fatalf("structured = %+v", structured)
	}
	worktrees, ok := structured["worktrees"].([]any)
	if !ok || len(worktrees) != 0 {
		t.Fatalf("worktrees = %#v", structured["worktrees"])
	}
	if text := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, "worktree はありません") {
		t.Fatalf("text = %q", text)
	}
}

// MCP 経由のライフサイクル E2E: worktree_create（実 git）→ worktree_list に現れる →
// worktree_remove（DestructiveHint 付き）で消え、structuredContent が RemoveResult の
// JSON 形（root_removed / repos）になる。
func TestWorktreeCreateListRemoveRoundTrip(t *testing.T) {
	isolate(t)
	reposDir := t.TempDir()
	worktreesDir := t.TempDir()
	testutil.CloneWithBranchNamed(t, reposDir, "main", "repo-a")
	t.Setenv("WT_REPOS_DIR", reposDir)
	t.Setenv("WT_WORKTREES_DIR", worktreesDir)
	cs := connect(t)

	created, err := cs.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "worktree_create",
		Arguments: map[string]any{"worktree_name": "feat-x", "repos": []string{"repo-a"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.IsError {
		t.Fatalf("worktree_create failed: %+v", created.Content)
	}

	listed, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "worktree_list"})
	if err != nil {
		t.Fatal(err)
	}
	worktrees := structuredOf(t, listed)["worktrees"].([]any)
	if len(worktrees) != 1 || worktrees[0].(map[string]any)["name"] != "feat-x" {
		t.Fatalf("worktrees = %#v", worktrees)
	}

	removed, err := cs.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "worktree_remove",
		Arguments: map[string]any{"name": "feat-x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if removed.IsError {
		t.Fatalf("worktree_remove failed: %+v", removed.Content)
	}
	structured := structuredOf(t, removed)
	if structured["root_removed"] != true || structured["worktree"] != "feat-x" {
		t.Fatalf("structured = %+v", structured)
	}
	repos, ok := structured["repos"].([]any)
	if !ok || len(repos) != 1 || repos[0].(map[string]any)["removed"] != true {
		t.Fatalf("repos = %#v", structured["repos"])
	}
	if _, err := os.Stat(filepath.Join(worktreesDir, "feat-x")); !os.IsNotExist(err) {
		t.Fatalf("worktree root should be gone: %v", err)
	}
}

// 存在しない worktree の worktree_remove は IsError のツール結果になる（プロトコル
// エラーにはならない）。
func TestWorktreeRemoveMissingIsToolError(t *testing.T) {
	isolate(t)
	t.Setenv("WT_REPOS_DIR", t.TempDir())
	t.Setenv("WT_WORKTREES_DIR", t.TempDir())
	cs := connect(t)

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "worktree_remove",
		Arguments: map[string]any{"name": "no-such-worktree"},
	})
	if err != nil {
		t.Fatalf("プロトコルエラーにしてはならない: %v", err)
	}
	if !res.IsError {
		t.Fatal("IsError が立つべき")
	}
}

// 不正な引数（空文字列の worktree_name）は IsError のツール結果になり、プロトコル
// エラーにはならない。
func TestInvalidArgumentIsToolError(t *testing.T) {
	isolate(t)
	cs := connect(t)

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "server_stop",
		Arguments: map[string]any{"worktree_name": ""},
	})
	if err != nil {
		t.Fatalf("プロトコルエラーにしてはならない: %v", err)
	}
	if !res.IsError {
		t.Fatal("IsError が立つべき")
	}
}
