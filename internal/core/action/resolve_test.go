package action

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vimrak-hal/worktree-integrator/internal/core/cmdspec"
	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
	"github.com/vimrak-hal/worktree-integrator/internal/core/server"
)

// noEnv は環境変数を一切設定しない getenv。
func noEnv(string) string { return "" }

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
		cfg, err := NewCreate("feat", nil, false, "", ov, file, env)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ReposDir != "/flag/repos" || cfg.WorktreesDir != "/flag/wt" || cfg.Remote != "upstream" || cfg.Concurrency != 9 {
			t.Fatalf("cfg = %+v", cfg)
		}
	})

	t.Run("フラグ無指定なら環境変数", func(t *testing.T) {
		cfg, err := NewCreate("feat", nil, false, "", Overrides{}, file, env)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ReposDir != "/env/repos" || cfg.WorktreesDir != "/env/wt" || cfg.Remote != "envremote" || cfg.Concurrency != 7 {
			t.Fatalf("cfg = %+v", cfg)
		}
	})

	t.Run("環境変数も無ければ設定ファイル", func(t *testing.T) {
		cfg, err := NewCreate("feat", nil, false, "", Overrides{}, file, noEnv)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ReposDir != "/cfg/repos" || cfg.WorktreesDir != "/cfg/wt" || cfg.Remote != "fork" || cfg.Concurrency != 3 {
			t.Fatalf("cfg = %+v", cfg)
		}
	})

	t.Run("すべて無ければ既定値", func(t *testing.T) {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skipf("home dir unavailable: %v", err)
		}
		cfg, err := NewCreate("feat", nil, false, "", Overrides{}, &config.File{}, noEnv)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ReposDir != filepath.Join(home, "repositories") {
			t.Errorf("ReposDir = %q", cfg.ReposDir)
		}
		if cfg.WorktreesDir != filepath.Join(home, "worktrees") {
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
	if _, err := NewCreate("feat", nil, false, "", Overrides{}, &config.File{}, envOf(map[string]string{"WT_CONCURRENCY": "abc"})); err == nil {
		t.Error("非整数の WT_CONCURRENCY はエラーになるべき")
	}
	if _, err := NewCreate("feat", nil, false, "", Overrides{}, &config.File{}, envOf(map[string]string{"WT_CONCURRENCY": "-1"})); err == nil {
		t.Error("負の WT_CONCURRENCY はエラーになるべき")
	}
	cfg, err := NewCreate("feat", nil, false, "", Overrides{}, &config.File{Concurrency: 5}, envOf(map[string]string{"WT_CONCURRENCY": "0"}))
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
	if _, err := NewCreate("feat", nil, false, "", Overrides{Concurrency: -1}, &config.File{}, noEnv); err == nil {
		t.Error("負の並列度はエラーになるべき")
	}
	cfg, err := NewCreate("feat", nil, false, "", Overrides{Concurrency: 0}, &config.File{}, noEnv)
	if err != nil {
		t.Fatalf("並列度 0（自動）は受理されるべき: %v", err)
	}
	if cfg.Concurrency != 0 {
		t.Errorf("Concurrency = %d", cfg.Concurrency)
	}
}

func TestNewCreateValidatesName(t *testing.T) {
	if _, err := NewCreate("bad name", nil, false, "", Overrides{}, &config.File{}, noEnv); err == nil {
		t.Fatal("expected validation error")
	}
}

// 明示されたリポジトリ名はパスコンポーネントとして検証され、--repo と --all の
// 同時指定は拒否される。
func TestNewCreateValidatesRepoSelection(t *testing.T) {
	if _, err := NewCreate("feat", []string{"../escape"}, false, "", Overrides{}, &config.File{}, noEnv); err == nil {
		t.Error("不正なリポジトリ名はエラーになるべき")
	}
	if _, err := NewCreate("feat", []string{"api"}, true, "", Overrides{}, &config.File{}, noEnv); err == nil {
		t.Error("--repo と --all の同時指定はエラーになるべき")
	}
	cfg, err := NewCreate("feat", []string{"api", "web"}, false, "", Overrides{}, &config.File{}, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Repos) != 2 || cfg.All {
		t.Fatalf("cfg = %+v", cfg)
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
	cfg, err := NewCreate("feat", nil, false, "flag-base", Overrides{}, file, noEnv)
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
	cfg, err = NewCreate("feat", nil, false, "", Overrides{}, file, noEnv)
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
	cfg, err = NewCreate("feat", nil, false, "", Overrides{}, &config.File{}, noEnv)
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
	cfg, err := NewCreate("feat", nil, false, "", Overrides{}, file, noEnv)
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
	cmd, err := NewServerCommand(Overrides{ReposDir: "/explicit/repos"}, file, noEnv, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.ReposDir != "/explicit/repos" || cmd.WorktreesDir != "/cfg/wt" {
		t.Fatalf("cmd = %+v", cmd)
	}
}

// NewServerCommand は Repos フィルタの各要素をパスコンポーネントとして検証する。
func TestNewServerCommandValidatesRepoNames(t *testing.T) {
	if _, err := NewServerCommand(Overrides{}, &config.File{}, noEnv, []string{"ok", "../bad"}); err == nil {
		t.Fatal("不正なリポジトリ名はエラーになるべき")
	}
	cmd, err := NewServerCommand(Overrides{}, &config.File{}, noEnv, []string{"api"})
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
	cmd, err := NewServerCommand(Overrides{}, &config.File{}, env, nil)
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
	cmd, err := NewServerCommand(Overrides{}, file, noEnv, nil)
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
	cfg, err := NewCreate("feat", nil, false, "", Overrides{Remote: "upstream"}, &config.File{Remote: "fork"}, env)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Remote != "upstream" {
		t.Fatalf("override should win: %q", cfg.Remote)
	}
	// フラグが無ければ環境変数。
	cfg, err = NewCreate("feat", nil, false, "", Overrides{}, &config.File{Remote: "fork"}, env)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Remote != "envremote" {
		t.Fatalf("env should win: %q", cfg.Remote)
	}
	// 環境変数も無ければファイル。
	cfg, err = NewCreate("feat", nil, false, "", Overrides{}, &config.File{Remote: "fork"}, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Remote != "fork" {
		t.Fatalf("file should win: %q", cfg.Remote)
	}
	// どれも無ければ "origin"。
	cfg, err = NewCreate("feat", nil, false, "", Overrides{}, &config.File{}, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Remote != "origin" {
		t.Fatalf("default should be origin: %q", cfg.Remote)
	}
}

// HOME を特定できない場合、既定ディレクトリの解決は（旧実装の相対パスへの静かな
// フォールバックではなく）エラーになる — 意図的な仕様変更。os.UserHomeDir は
// HOME 環境変数を参照するため、ここだけは t.Setenv で HOME を消す。
func TestDefaultDirErrorsWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	_, err := ReposDir("", &config.File{}, noEnv)
	if err == nil {
		t.Fatal("HOME 不明時はエラーになるべき")
	}
	if !strings.Contains(err.Error(), "ホームディレクトリ") {
		t.Errorf("error = %q", err)
	}
}
