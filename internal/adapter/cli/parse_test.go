package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/vimrak-hal/worktree-integrator/internal/core/action"
)

// Parse は I/O を行わない純関数のため、旧テストの isolate（HOME / WT_* の差し替え
// 儀式）は不要になった。ここでは「引数 → Invocation」の写像だけを検証し、設定
// ファイルや環境変数とのマージは core/action のテストが担う。

func parse(t *testing.T, args ...string) Invocation {
	t.Helper()
	inv, err := Parse(args)
	if err != nil {
		t.Fatalf("Parse(%v) = %v", args, err)
	}
	return inv
}

// createOf、serverOf、aliasOf は、inv が期待されるバリアントであることを表明する。
func createOf(t *testing.T, inv Invocation) Create {
	t.Helper()
	c, ok := inv.(Create)
	if !ok {
		t.Fatalf("expected a Create invocation, got %T", inv)
	}
	return c
}

func serverOf(t *testing.T, inv Invocation) Server {
	t.Helper()
	c, ok := inv.(Server)
	if !ok {
		t.Fatalf("expected a Server invocation, got %T", inv)
	}
	return c
}

func aliasOf(t *testing.T, inv Invocation) Alias {
	t.Helper()
	c, ok := inv.(Alias)
	if !ok {
		t.Fatalf("expected an Alias invocation, got %T", inv)
	}
	return c
}

func TestBareNameResolvesToCreateWithFlags(t *testing.T) {
	inv := parse(t, "--repos-dir", "/r", "--worktrees-dir", "/w", "--remote", "upstream", "-j", "4", "feature-1")
	c := createOf(t, inv)
	if c.Name != "feature-1" {
		t.Fatalf("name = %q", c.Name)
	}
	want := action.Overrides{ReposDir: "/r", WorktreesDir: "/w", Remote: "upstream", Concurrency: 4}
	if c.Ov != want {
		t.Fatalf("ov = %+v, want %+v", c.Ov, want)
	}
}

func TestExplicitCreateSubcommand(t *testing.T) {
	if createOf(t, parse(t, "create", "feat")).Name != "feat" {
		t.Fatal("name should be feat")
	}
}

// フラグ未指定の Overrides はゼロ値のまま（解決は action 側の責務）。
func TestFallsBackToZeroOverrides(t *testing.T) {
	c := createOf(t, parse(t, "feat"))
	if c.Ov != (action.Overrides{}) {
		t.Fatalf("ov = %+v", c.Ov)
	}
	if len(c.Repos) != 0 || c.All {
		t.Fatalf("selection = %+v", c)
	}
}

// --base は Create 起動要求の Base フィールドへそのまま乗る（未指定はゼロ値）。
func TestCreateBaseFlag(t *testing.T) {
	c := createOf(t, parse(t, "create", "feat", "--base", "develop"))
	if c.Base != "develop" {
		t.Fatalf("base = %q, want develop", c.Base)
	}
	if createOf(t, parse(t, "feat")).Base != "" {
		t.Fatal("omitted --base should leave Base at its zero value")
	}
}

// --repo（繰り返し可）と --all は Create 起動要求へそのまま乗る。
func TestCreateRepoAndAllFlags(t *testing.T) {
	c := createOf(t, parse(t, "create", "feat", "--repo", "api", "--repo", "web"))
	if !reflect.DeepEqual(c.Repos, []string{"api", "web"}) || c.All {
		t.Fatalf("c = %+v", c)
	}
	c = createOf(t, parse(t, "feat", "--all"))
	if !c.All || len(c.Repos) != 0 {
		t.Fatalf("c = %+v", c)
	}
}

// 旧予約語（repos / list / enter / remove / doctor）はコマンドとして実装され、
// 素の先頭トークンはサブコマンドとして解釈される（worktree 名としては扱われない）。
func TestFormerReservedWordsAreNowSubcommands(t *testing.T) {
	if _, ok := parse(t, "list").(List); !ok {
		t.Fatal("`list` should parse as the List invocation")
	}
	if _, ok := parse(t, "doctor").(Doctor); !ok {
		t.Fatal("`doctor` should parse as the Doctor invocation")
	}
	if _, ok := parse(t, "repos").(Repos); !ok {
		t.Fatal("`repos` should parse as the Repos invocation")
	}
	// enter / remove は名前が必須（素の名前として create に化けない）。
	if _, err := Parse([]string{"enter"}); err == nil || !strings.Contains(err.Error(), "arg") {
		t.Fatalf("`enter` without a name should be an argument error, got %v", err)
	}
	if _, err := Parse([]string{"remove"}); err == nil || !strings.Contains(err.Error(), "arg") {
		t.Fatalf("`remove` without a name should be an argument error, got %v", err)
	}
	// サブコマンド名と同名の worktree は create の明示で作れる。
	if createOf(t, parse(t, "create", "list")).Name != "list" {
		t.Fatal("explicit create should accept a subcommand word as the name")
	}
}

// tree 系コマンドのフラグは Invocation バリアントへそのまま乗る。
func TestParsesTreeCommands(t *testing.T) {
	if l, ok := parse(t, "list", "--json").(List); !ok || !l.Json {
		t.Fatalf("list --json = %#v", l)
	}
	e, ok := parse(t, "enter", "feat-x").(Enter)
	if !ok || e.Name.String() != "feat-x" {
		t.Fatalf("enter = %#v", e)
	}
	r, ok := parse(t, "remove", "feat-x", "--force", "--keep-branch").(Remove)
	if !ok || r.Name.String() != "feat-x" || !r.Force || !r.KeepBranch {
		t.Fatalf("remove = %#v", r)
	}
	if r := parse(t, "remove", "feat-x").(Remove); r.Force || r.KeepBranch {
		t.Fatalf("remove without flags = %#v", r)
	}
	d, ok := parse(t, "doctor", "--fix", "--json").(Doctor)
	if !ok || !d.Fix || !d.Json {
		t.Fatalf("doctor = %#v", d)
	}
	// 不正な worktree 名は解析の時点で拒否される（enter / remove）。
	if _, err := Parse([]string{"enter", "bad name!"}); err == nil {
		t.Fatal("enter with an invalid name should be an error")
	}
	if _, err := Parse([]string{"remove", "bad name!"}); err == nil {
		t.Fatal("remove with an invalid name should be an error")
	}
}

// injectCreate のトークン走査: 値付きフラグ（分離形式 / = 形式）、bool フラグ、
// -- 終端の混在をテーブルで固定する。
func TestInjectCreateTable(t *testing.T) {
	// valueFlags は create コマンドのフラグ定義から導出される（単一情報源）。
	vf := map[string]bool{
		"--repo": true, "--repos-dir": true, "--worktrees-dir": true,
		"--remote": true, "--concurrency": true, "-j": true, "--base": true,
	}
	tests := []struct {
		name    string
		args    []string
		want    []string
		wantErr bool
	}{
		{"素の名前", []string{"feat"}, []string{"create", "feat"}, false},
		{"分離形式の値フラグの後の名前", []string{"-j", "4", "feat"}, []string{"create", "-j", "4", "feat"}, false},
		{"=形式は値を消費しない", []string{"--remote=origin", "feat"}, []string{"create", "--remote=origin", "feat"}, false},
		{"boolフラグは値を消費しない", []string{"--all", "feat"}, []string{"create", "--all", "feat"}, false},
		{"混在", []string{"--repos-dir", "/r", "--all", "--remote=up", "feat"},
			[]string{"create", "--repos-dir", "/r", "--all", "--remote=up", "feat"}, false},
		{"サブコマンドは素通し", []string{"server", "status"}, []string{"server", "status"}, false},
		{"実装済みの旧予約語も素通し", []string{"doctor"}, []string{"doctor"}, false},
		{"フラグの値がサブコマンド名でも素通しされない", []string{"--remote", "server", "feat"},
			[]string{"create", "--remote", "server", "feat"}, false},
		{"-- 以降は位置引数と解釈しない", []string{"--", "feat"}, []string{"--", "feat"}, false},
		{"位置引数なし", []string{"--all"}, []string{"--all"}, false},
		{"予約語はエラー", []string{"future-cmd"}, nil, true},
	}
	// 予約語の仕組み自体を固定する（現在の予約語集合は空のため、テスト用に 1 語
	// 追加して案内エラーの経路を通す）。
	reservedSubcommand["future-cmd"] = true
	t.Cleanup(func() { delete(reservedSubcommand, "future-cmd") })
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := injectCreate(tt.args, vf)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParsesServerSwitch(t *testing.T) {
	inv := parse(t, "server", "switch", "feat-x", "--repo", "backend", "--repo", "frontend", "--restart")
	srv := serverOf(t, inv)
	k, ok := srv.Kind.(action.SwitchKind)
	if !ok {
		t.Fatalf("expected SwitchKind, got %T", srv.Kind)
	}
	if k.Name.String() != "feat-x" || k.Restart != true {
		t.Fatalf("kind = %+v", k)
	}
	// Repos フィルタは Kind ではなく Server 起動要求に乗る。
	if !reflect.DeepEqual(srv.Repos, []string{"backend", "frontend"}) {
		t.Fatalf("repos = %v", srv.Repos)
	}
}

func TestActivateAliasMapsToSwitch(t *testing.T) {
	if _, ok := serverOf(t, parse(t, "server", "activate", "feat-x")).Kind.(action.SwitchKind); !ok {
		t.Fatal("activate should map to switch")
	}
}

func TestParsesStatusStopLogs(t *testing.T) {
	status := serverOf(t, parse(t, "server", "status"))
	if _, ok := status.Kind.(action.StatusKind); !ok {
		t.Fatal("status op")
	}
	if status.Json {
		t.Fatal("status without --json must not set Json")
	}
	stop, ok := serverOf(t, parse(t, "server", "stop", "feat-x")).Kind.(action.StopKind)
	if !ok {
		t.Fatalf("expected StopKind")
	}
	if one, ok := stop.Scope.(action.OneWorktree); !ok || one.Name.String() != "feat-x" {
		t.Fatalf("stop scope = %+v", stop.Scope)
	}
	srv := serverOf(t, parse(t, "server", "logs", "feat-x", "-n", "120", "-f"))
	logs, ok := srv.Kind.(action.LogsKind)
	if !ok {
		t.Fatalf("expected LogsKind")
	}
	if one, ok := logs.Scope.(action.OneWorktree); !ok || one.Name.String() != "feat-x" {
		t.Fatalf("logs scope = %+v", logs.Scope)
	}
	if logs.Lines != 120 {
		t.Fatalf("logs = %+v", logs)
	}
	// -f は action の語彙（LogsKind）ではなく CLI の Server 起動要求に乗る
	// （MCP から follow へは型レベルで到達不能 — 意図的な仕様変更）。
	if !srv.FollowLogs {
		t.Fatalf("srv = %+v, want FollowLogs", srv)
	}
}

// server status --json は表示形式の選択として Server 起動要求に乗る。
func TestParsesStatusJson(t *testing.T) {
	srv := serverOf(t, parse(t, "server", "status", "--json"))
	if _, ok := srv.Kind.(action.StatusKind); !ok {
		t.Fatalf("expected StatusKind, got %T", srv.Kind)
	}
	if !srv.Json {
		t.Fatalf("srv = %+v, want Json", srv)
	}
}

// 引数の省略のみが「全 worktree 対象」を意味する。
func TestStopLogsOmittedNameScopesToAllWorktrees(t *testing.T) {
	stop := serverOf(t, parse(t, "server", "stop")).Kind.(action.StopKind)
	if _, ok := stop.Scope.(action.AllWorktrees); !ok {
		t.Fatalf("omitted stop name should scope to all worktrees, got %T", stop.Scope)
	}
	logs := serverOf(t, parse(t, "server", "logs")).Kind.(action.LogsKind)
	if _, ok := logs.Scope.(action.AllWorktrees); !ok {
		t.Fatalf("omitted logs name should scope to all worktrees, got %T", logs.Scope)
	}
	if logs.Lines != 50 {
		t.Fatalf("default lines = %d, want 50", logs.Lines)
	}
}

// 明示的な空文字列は不正名エラー（意図的な仕様変更: 旧実装は空文字列を全件対象へ
// 正規化していた）。
func TestStopLogsEmptyNameIsError(t *testing.T) {
	if _, err := Parse([]string{"server", "stop", ""}); err == nil {
		t.Fatal("server stop \"\" should be an error")
	}
	if _, err := Parse([]string{"server", "logs", ""}); err == nil {
		t.Fatal("server logs \"\" should be an error")
	}
}

func TestParsesAlias(t *testing.T) {
	set, ok := aliasOf(t, parse(t, "alias", "set", "feat-x", "ABC-123: title")).Kind.(action.AliasSet)
	if !ok || set.Name.String() != "feat-x" || set.Value != "ABC-123: title" {
		t.Fatalf("set = %+v", set)
	}
	if _, ok := aliasOf(t, parse(t, "alias", "ls")).Kind.(action.AliasList); !ok {
		t.Fatal("ls op")
	}
	if got, ok := aliasOf(t, parse(t, "alias", "rm", "feat-x")).Kind.(action.AliasRemove); !ok || got.Name.String() != "feat-x" {
		t.Fatalf("rm = %+v", got)
	}
}

// `alias get` は廃止された（list に統合 — 意図的な仕様変更）。cobra は未知の
// サブコマンドとしてエラーにする。
func TestAliasGetIsRemoved(t *testing.T) {
	if _, err := Parse([]string{"alias", "get", "feat-x"}); err == nil {
		t.Fatal("alias get should no longer parse")
	}
}

func TestAliasSetRejectsInvalidName(t *testing.T) {
	if _, err := Parse([]string{"alias", "set", "bad name", "x"}); err == nil {
		t.Fatal("expected error for invalid name")
	}
}

// フラグスコープの整理: --remote / -j は create 専用、--repos-dir / --worktrees-dir は
// create と server 系のみで、alias 系はフラグを受け付けない（受理されて黙って無視
// される経路を消した）。
func TestFlagScopes(t *testing.T) {
	// server 系はディレクトリのフラグを受け付ける。
	srv := serverOf(t, parse(t, "server", "status", "--worktrees-dir", "/w"))
	if srv.Ov.WorktreesDir != "/w" {
		t.Fatalf("ov = %+v", srv.Ov)
	}
	// server 系は --remote / -j を受け付けない。
	if _, err := Parse([]string{"server", "status", "--remote", "up"}); err == nil {
		t.Fatal("server should not accept --remote")
	}
	if _, err := Parse([]string{"server", "status", "-j", "4"}); err == nil {
		t.Fatal("server should not accept -j")
	}
	// alias 系はどのフラグも受け付けない。
	if _, err := Parse([]string{"alias", "list", "--repos-dir", "/r"}); err == nil {
		t.Fatal("alias should not accept --repos-dir")
	}
}

func TestMCPInvocation(t *testing.T) {
	if _, ok := parse(t, "mcp").(RunMCP); !ok {
		t.Fatal("expected RunMCP for the mcp subcommand")
	}
}

func TestUIInvocation(t *testing.T) {
	if _, ok := parse(t, "ui").(RunUI); !ok {
		t.Fatal("expected RunUI for the ui subcommand")
	}
}

// `config check` は ConfigCheck を返す。config は既知のサブコマンドであり
// （予約語ではない）、引数無しの `config` は cobra のヘルプに落ちる。
func TestConfigCheckInvocation(t *testing.T) {
	if _, ok := parse(t, "config", "check").(ConfigCheck); !ok {
		t.Fatal("expected ConfigCheck for `config check`")
	}
	if _, ok := parse(t, "config").(HelpShown); !ok {
		t.Fatal("bare `config` should show help, not be treated as a worktree name")
	}
}

// --help / --version は HelpShown として返り、テキストは（プロセスの stdout ではなく）
// バリアントに載る。main が stdout へ書く。
func TestHelpAndVersionReturnHelpShown(t *testing.T) {
	help, ok := parse(t, "--help").(HelpShown)
	if !ok {
		t.Fatalf("expected HelpShown, got %T", parse(t, "--help"))
	}
	if !strings.Contains(help.Text, "Usage:") {
		t.Fatalf("help text = %q", help.Text)
	}
	version, ok := parse(t, "--version").(HelpShown)
	if !ok || strings.TrimSpace(version.Text) == "" {
		t.Fatalf("version = %#v", version)
	}
	// サブコマンド未指定の `server` も cobra のヘルプとして HelpShown になる。
	if _, ok := parse(t, "server").(HelpShown); !ok {
		t.Fatal("bare `server` should return HelpShown")
	}
}

func TestRequiresAName(t *testing.T) {
	if _, err := Parse([]string{"--remote", "origin"}); err == nil {
		t.Fatal("expected error when no name given")
	}
}
