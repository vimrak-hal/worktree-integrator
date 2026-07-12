package action

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vimrak-hal/worktree-integrator/internal/core/cmdspec"
	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
	"github.com/vimrak-hal/worktree-integrator/internal/core/server"
)

// noEnv は環境変数を一切設定しない getenv。
func noEnv(string) string { return "" }

// homeDir はテスト用の固定ホームディレクトリ。既定ディレクトリ（~/repositories・
// ~/worktrees）の解決を os.UserHomeDir に依存させないため、home 注入として渡す。
const homeDir = "/home/tester"

// testHome は固定のホームディレクトリを返す home 注入（resolve が os.UserHomeDir を
// 直読みしない、という契約の裏返し）。
func testHome() (string, error) { return homeDir, nil }

// envOf はマップに基づく getenv を返す。t.Setenv の儀式なしに環境変数の段を
// テストできる（resolve が os.Getenv を直読みしない、という契約の裏返し）。
func envOf(m map[string]string) func(string) string {
	return func(key string) string { return m[key] }
}

// 4 段の優先順位「フラグ > 環境変数 > 設定ファイル > 既定値」を、環境変数を持つ
// 全スカラー項目（ReposDir / WorktreesDir / Remote / Concurrency）について
// テーブルで固定する。
func TestResolutionPrecedenceTable(t *testing.T) {
	file := &config.File{
		ReposDir:     "/cfg/repos",
		WorktreesDir: "/cfg/wt",
		Remote:       "fork",
		Concurrency:  3,
	}
	env := envOf(map[string]string{
		"WT_REPOS_DIR":     "/env/repos",
		"WT_WORKTREES_DIR": "/env/wt",
		"WT_REMOTE":        "envremote",
		"WT_CONCURRENCY":   "7",
	})

	t.Run("フラグが最優先", func(t *testing.T) {
		ov := Overrides{ReposDir: "/flag/repos", WorktreesDir: "/flag/wt", Remote: "upstream", Concurrency: 9}
		cfg, err := NewCreate("feat", nil, false, "", ov, file, env, testHome)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ReposDir != "/flag/repos" || cfg.WorktreesDir != "/flag/wt" || cfg.Remote != "upstream" || cfg.Concurrency != 9 {
			t.Fatalf("cfg = %+v", cfg)
		}
	})

	t.Run("フラグ無指定なら環境変数", func(t *testing.T) {
		cfg, err := NewCreate("feat", nil, false, "", Overrides{}, file, env, testHome)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ReposDir != "/env/repos" || cfg.WorktreesDir != "/env/wt" || cfg.Remote != "envremote" || cfg.Concurrency != 7 {
			t.Fatalf("cfg = %+v", cfg)
		}
	})

	t.Run("環境変数も無ければ設定ファイル", func(t *testing.T) {
		cfg, err := NewCreate("feat", nil, false, "", Overrides{}, file, noEnv, testHome)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ReposDir != "/cfg/repos" || cfg.WorktreesDir != "/cfg/wt" || cfg.Remote != "fork" || cfg.Concurrency != 3 {
			t.Fatalf("cfg = %+v", cfg)
		}
	})

	t.Run("すべて無ければ既定値", func(t *testing.T) {
		cfg, err := NewCreate("feat", nil, false, "", Overrides{}, &config.File{}, noEnv, testHome)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ReposDir != filepath.Join(homeDir, "repositories") {
			t.Errorf("ReposDir = %q", cfg.ReposDir)
		}
		if cfg.WorktreesDir != filepath.Join(homeDir, "worktrees") {
			t.Errorf("WorktreesDir = %q", cfg.WorktreesDir)
		}
		if cfg.Remote != "origin" {
			t.Errorf("Remote = %q", cfg.Remote)
		}
		if cfg.Concurrency != 0 {
			t.Errorf("Concurrency = %d, want 0 (自動)", cfg.Concurrency)
		}
	})
}

// WT_CONCURRENCY は整数として解釈され、不正な値・負値はエラーになる。
// "0" は「自動 = 未指定」として次の段（設定ファイル）へフォールスルーする。
func TestConcurrencyEnvParsing(t *testing.T) {
	if _, err := NewCreate("feat", nil, false, "", Overrides{}, &config.File{}, envOf(map[string]string{"WT_CONCURRENCY": "abc"}), testHome); err == nil {
		t.Error("非整数の WT_CONCURRENCY はエラーになるべき")
	}
	if _, err := NewCreate("feat", nil, false, "", Overrides{}, &config.File{}, envOf(map[string]string{"WT_CONCURRENCY": "-1"}), testHome); err == nil {
		t.Error("負の WT_CONCURRENCY はエラーになるべき")
	}
	cfg, err := NewCreate("feat", nil, false, "", Overrides{}, &config.File{Concurrency: 5}, envOf(map[string]string{"WT_CONCURRENCY": "0"}), testHome)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Concurrency != 5 {
		t.Errorf("WT_CONCURRENCY=0 は未指定としてファイル値 5 を採るべき, got %d", cfg.Concurrency)
	}
}

// 負の並列度フラグはエラー。0 は「自動」として受理される（旧実装は 0 を拒否して
// いたが、pointer-as-optional の廃止に伴い 0 = 自動へ変更した — 意図的な仕様変更）。
func TestNewCreateConcurrencyValidation(t *testing.T) {
	if _, err := NewCreate("feat", nil, false, "", Overrides{Concurrency: -1}, &config.File{}, noEnv, testHome); err == nil {
		t.Error("負の並列度はエラーになるべき")
	}
	cfg, err := NewCreate("feat", nil, false, "", Overrides{Concurrency: 0}, &config.File{}, noEnv, testHome)
	if err != nil {
		t.Fatalf("並列度 0（自動）は受理されるべき: %v", err)
	}
	if cfg.Concurrency != 0 {
		t.Errorf("Concurrency = %d", cfg.Concurrency)
	}
}

func TestNewCreateValidatesName(t *testing.T) {
	if _, err := NewCreate("bad name", nil, false, "", Overrides{}, &config.File{}, noEnv, testHome); err == nil {
		t.Fatal("expected validation error")
	}
}

// 明示されたリポジトリ名はパスコンポーネントとして検証され、--repo と --all の
// 同時指定は拒否される。
func TestNewCreateValidatesRepoSelection(t *testing.T) {
	if _, err := NewCreate("feat", []string{"../escape"}, false, "", Overrides{}, &config.File{}, noEnv, testHome); err == nil {
		t.Error("不正なリポジトリ名はエラーになるべき")
	}
	if _, err := NewCreate("feat", []string{"api"}, true, "", Overrides{}, &config.File{}, noEnv, testHome); err == nil {
		t.Error("--repo と --all の同時指定はエラーになるべき")
	}
	cfg, err := NewCreate("feat", []string{"api", "web"}, false, "", Overrides{}, &config.File{}, noEnv, testHome)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Repos) != 2 || cfg.All {
		t.Fatalf("cfg = %+v", cfg)
	}
}

// validateBase は git fetch の位置引数（ブランチ名）へ渡る base 指定を検証する。
// リテラル "auto" と通常のブランチ名は許可し、先頭 '-' のオプション化・パス
// トラバーサル・空文字は拒否する。
func TestValidateBase(t *testing.T) {
	for _, b := range []string{"auto", "main", "feature/x"} {
		if err := validateBase(b); err != nil {
			t.Errorf("validateBase(%q) = %v, want nil", b, err)
		}
	}
	for _, b := range []string{"--upload-pack=/bin/true", "-x", "", "feature/-x", "../escape"} {
		if err := validateBase(b); err == nil {
			t.Errorf("validateBase(%q) = nil, want error", b)
		}
	}
}

// validateRemote は git fetch の位置引数（リモート名）へ渡る remote 指定を、単一
// セグメントとして検証する。"origin" は許可し、先頭 '-'・"/"・空文字は拒否する。
func TestValidateRemote(t *testing.T) {
	if err := validateRemote("origin"); err != nil {
		t.Errorf("validateRemote(origin) = %v, want nil", err)
	}
	for _, r := range []string{"-origin", "", "a/b", "--upload-pack=x"} {
		if err := validateRemote(r); err == nil {
			t.Errorf("validateRemote(%q) = nil, want error", r)
		}
	}
}

// NewCreate は base・remote の参照しうる全ソース（--base フラグ・remote・
// [defaults].base・[repos.<name>].base）を検証し、違反時はエラーを返す。
func TestNewCreateValidatesBaseAndRemote(t *testing.T) {
	// 不正な --base フラグ。
	if _, err := NewCreate("feat", nil, false, "--upload-pack=/bin/true", Overrides{}, &config.File{}, noEnv, testHome); err == nil {
		t.Error("不正な --base はエラーになるべき")
	}
	// 不正な remote（フラグ）。
	if _, err := NewCreate("feat", nil, false, "", Overrides{Remote: "-origin"}, &config.File{}, noEnv, testHome); err == nil {
		t.Error("不正な remote はエラーになるべき")
	}
	// 不正な [repos.<name>].base。
	badRepo := &config.File{Repos: map[string]config.RepoConfig{"api": {Base: "-x"}}}
	if _, err := NewCreate("feat", nil, false, "", Overrides{}, badRepo, noEnv, testHome); err == nil {
		t.Error("不正な [repos.api].base はエラーになるべき")
	}
	// 不正な [defaults].base。
	badDefaults := &config.File{Defaults: config.Defaults{Base: "--exec=x"}}
	if _, err := NewCreate("feat", nil, false, "", Overrides{}, badDefaults, noEnv, testHome); err == nil {
		t.Error("不正な [defaults].base はエラーになるべき")
	}
	// 正常系: auto / main / feature/x・origin はいずれも通る。
	ok := &config.File{
		Defaults: config.Defaults{Base: "main"},
		Repos:    map[string]config.RepoConfig{"api": {Base: "feature/x"}},
	}
	if _, err := NewCreate("feat", nil, false, "auto", Overrides{Remote: "origin"}, ok, noEnv, testHome); err != nil {
		t.Errorf("正常な base・remote は通るべき: %v", err)
	}
}

// base の解決順序: --base フラグ / MCP の base パラメータ（Create.Base）>
// [repos.<repo>].base > [defaults].base > "auto"。
func TestCreateBaseForResolutionOrder(t *testing.T) {
	file := &config.File{
		Defaults: config.Defaults{Base: "develop"},
		Repos: map[string]config.RepoConfig{
			"api": {Base: "release"},
		},
	}

	// フラグ最優先。
	cfg, err := NewCreate("feat", nil, false, "flag-base", Overrides{}, file, noEnv, testHome)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.BaseFor("api"); got != "flag-base" {
		t.Fatalf("BaseFor(api) = %q, want flag-base", got)
	}
	if got := cfg.BaseFor("web"); got != "flag-base" {
		t.Fatalf("BaseFor(web) = %q, want flag-base (applies to every repo)", got)
	}

	// フラグ無指定なら repos.<repo>.base。
	cfg, err = NewCreate("feat", nil, false, "", Overrides{}, file, noEnv, testHome)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.BaseFor("api"); got != "release" {
		t.Fatalf("BaseFor(api) = %q, want release", got)
	}
	// repos.<repo>.base の無いリポジトリは defaults.base。
	if got := cfg.BaseFor("web"); got != "develop" {
		t.Fatalf("BaseFor(web) = %q, want develop", got)
	}

	// defaults.base も無ければ "auto"。
	cfg, err = NewCreate("feat", nil, false, "", Overrides{}, &config.File{}, noEnv, testHome)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.BaseFor("anything"); got != "auto" {
		t.Fatalf("BaseFor(anything) = %q, want auto", got)
	}
}

// CopyPlanFor は defaults.copy と repos.<repo>.copy をマージして解決する。
func TestCreateCopyPlanForMerges(t *testing.T) {
	file := &config.File{
		Defaults: config.Defaults{Copy: config.CopySpec{Paths: []string{".env"}}},
		Repos: map[string]config.RepoConfig{
			"api": {Copy: config.CopySpec{Paths: []string{"backend/.env"}, Gitignored: true}},
		},
	}
	cfg, err := NewCreate("feat", nil, false, "", Overrides{}, file, noEnv, testHome)
	if err != nil {
		t.Fatal(err)
	}
	plan := cfg.CopyPlanFor("api")
	if len(plan.Paths) != 2 || !plan.Gitignored {
		t.Fatalf("plan = %+v", plan)
	}
	other := cfg.CopyPlanFor("other")
	if len(other.Paths) != 1 || other.Paths[0] != ".env" || other.Gitignored {
		t.Fatalf("other = %+v", other)
	}
}

func TestNewServerCommandResolvesDirs(t *testing.T) {
	file := &config.File{WorktreesDir: "/cfg/wt"}
	cmd, err := NewServerCommand(Overrides{ReposDir: "/explicit/repos"}, file, noEnv, testHome, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.ReposDir != "/explicit/repos" || cmd.WorktreesDir != "/cfg/wt" {
		t.Fatalf("cmd = %+v", cmd)
	}
}

// NewServerCommand は Repos フィルタの各要素をパスコンポーネントとして検証する。
func TestNewServerCommandValidatesRepoNames(t *testing.T) {
	if _, err := NewServerCommand(Overrides{}, &config.File{}, noEnv, testHome, []string{"ok", "../bad"}); err == nil {
		t.Fatal("不正なリポジトリ名はエラーになるべき")
	}
	cmd, err := NewServerCommand(Overrides{}, &config.File{}, noEnv, testHome, []string{"api"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cmd.Repos) != 1 || cmd.Repos[0] != "api" {
		t.Fatalf("Repos = %v", cmd.Repos)
	}
}

// NewServerCommand は環境変数（getenv 注入）からディレクトリを解決する。操作
// （ServerKind）はコマンドに埋め込まれず、App の型付きメソッドへ別途渡される。
func TestNewServerCommandResolvesFromEnv(t *testing.T) {
	env := envOf(map[string]string{"WT_REPOS_DIR": "/env/repos", "WT_WORKTREES_DIR": "/env/wt"})
	cmd, err := NewServerCommand(Overrides{}, &config.File{}, env, testHome, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.ReposDir != "/env/repos" || cmd.WorktreesDir != "/env/wt" {
		t.Fatalf("dirs = %q, %q", cmd.ReposDir, cmd.WorktreesDir)
	}
}

// NewServerCommand は file.Repos[*].Servers を server.Config へ集約する
// （旧トップレベル [servers] 相当）。
func TestNewServerCommandAggregatesServersFromRepos(t *testing.T) {
	file := &config.File{
		Repos: map[string]config.RepoConfig{
			"api": {Servers: map[string]server.Spec{"backend": {Start: cmdspec.FromString("run")}}},
		},
	}
	cmd, err := NewServerCommand(Overrides{}, file, noEnv, testHome, nil)
	if err != nil {
		t.Fatal(err)
	}
	repo, ok := cmd.Servers.GetRepo("api")
	if !ok || repo["backend"].Start.Script() != "run" {
		t.Fatalf("servers = %+v", cmd.Servers)
	}
}

// remote の解決に WT_REMOTE の段が入ったことを固定する。
func TestRemoteResolutionOrder(t *testing.T) {
	env := envOf(map[string]string{"WT_REMOTE": "envremote"})
	// フラグが最優先。
	cfg, err := NewCreate("feat", nil, false, "", Overrides{Remote: "upstream"}, &config.File{Remote: "fork"}, env, testHome)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Remote != "upstream" {
		t.Fatalf("override should win: %q", cfg.Remote)
	}
	// フラグが無ければ環境変数。
	cfg, err = NewCreate("feat", nil, false, "", Overrides{}, &config.File{Remote: "fork"}, env, testHome)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Remote != "envremote" {
		t.Fatalf("env should win: %q", cfg.Remote)
	}
	// 環境変数も無ければファイル。
	cfg, err = NewCreate("feat", nil, false, "", Overrides{}, &config.File{Remote: "fork"}, noEnv, testHome)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Remote != "fork" {
		t.Fatalf("file should win: %q", cfg.Remote)
	}
	// どれも無ければ "origin"。
	cfg, err = NewCreate("feat", nil, false, "", Overrides{}, &config.File{}, noEnv, testHome)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Remote != "origin" {
		t.Fatalf("default should be origin: %q", cfg.Remote)
	}
}

// home（os.UserHomeDir の注入）がホームディレクトリを特定できない場合、既定ディレクトリ
// の解決は（旧実装の相対パスへの静かなフォールバックではなく）エラーになる — 意図的な
// 仕様。home を注入するため、環境変数の差し替え（t.Setenv）は要らない。
func TestDefaultDirErrorsWithoutHome(t *testing.T) {
	failHome := func() (string, error) { return "", fmt.Errorf("ホーム不明") }
	_, err := ReposDir("", &config.File{}, noEnv, failHome)
	if err == nil {
		t.Fatal("home 不明時はエラーになるべき")
	}
	if !strings.Contains(err.Error(), "ホームディレクトリ") {
		t.Errorf("error = %q", err)
	}
}
