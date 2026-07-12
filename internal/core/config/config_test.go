package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMissingFileIsEmpty(t *testing.T) {
	f, err := LoadFrom(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if f.ReposDir != "" || f.WorktreesDir != "" || f.Remote != "" {
		t.Fatalf("expected all unset: %+v", f)
	}
}

func TestParsesDirsAndRemote(t *testing.T) {
	f, err := LoadFrom(writeConfig(t, `
repos_dir = "/srv/repos"
worktrees_dir = "/srv/worktrees"
remote = "upstream"
`))
	if err != nil {
		t.Fatal(err)
	}
	if f.ReposDir != "/srv/repos" {
		t.Fatalf("repos_dir = %v", f.ReposDir)
	}
	if f.WorktreesDir != "/srv/worktrees" {
		t.Fatalf("worktrees_dir = %v", f.WorktreesDir)
	}
	if f.Remote != "upstream" {
		t.Fatalf("remote = %v", f.Remote)
	}
}

// concurrency は 0（またはキー省略）が自動決定を意味し、負値は validateAll が拒否する。
func TestParsesConcurrency(t *testing.T) {
	f, err := LoadFrom(writeConfig(t, "concurrency = 8\n"))
	if err != nil {
		t.Fatal(err)
	}
	if f.Concurrency != 8 {
		t.Fatalf("concurrency = %d", f.Concurrency)
	}
	if f, err := LoadFrom(writeConfig(t, "remote = \"origin\"\n")); err != nil || f.Concurrency != 0 {
		t.Fatalf("omitted concurrency should be 0 (automatic): %d, %v", f.Concurrency, err)
	}
	if _, err := LoadFrom(writeConfig(t, "concurrency = -1\n")); err == nil ||
		!strings.Contains(err.Error(), "concurrency") {
		t.Fatalf("negative concurrency should be rejected, got %v", err)
	}
}

func TestPartialFileLeavesOtherFieldsUnset(t *testing.T) {
	f, err := LoadFrom(writeConfig(t, "worktrees_dir = \"/elsewhere\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if f.ReposDir != "" {
		t.Fatal("repos_dir should be unset")
	}
	if f.WorktreesDir != "/elsewhere" {
		t.Fatalf("worktrees_dir = %v", f.WorktreesDir)
	}
}

func TestUnknownKeysRejected(t *testing.T) {
	_, err := LoadFrom(writeConfig(t, "nope = true\n"))
	if err == nil || !strings.Contains(err.Error(), "解析できません") {
		t.Fatalf("expected parse error, got %v", err)
	}
}

// (a) スキーマ v2 の Load: 設計書 §5 の TOML 例そのものをパースできることを確認する。
func TestLoadsSchemaV2Example(t *testing.T) {
	f, err := LoadFrom(writeConfig(t, `
repos_dir     = "~/repositories"
worktrees_dir = "~/worktrees"
remote        = "origin"
concurrency   = 0

[defaults]
base = "auto"

[defaults.copy]
paths = [".env"]

[[hooks.before]]
name         = "jira-title"
command      = "echo hi"
timeout_secs = 60
allow_failure = true

[[hooks.after]]
name    = "cmux-open"
command = "~/hooks/cmux-open-worktree.sh"

[repos.api]
base = "develop"

[repos.api.copy]
gitignored = true
exclude    = ["tmp/cache"]

[repos.api.servers.backend]
start           = "bin/rails s -p 3000"
dir             = "backend"
setup           = ["bundle install", "bin/rails db:setup"]
on_activate     = "echo activate"
on_switch       = "echo switch"
stop_grace_secs = 10

[repos.web.servers.frontend]
start = "pnpm dev"
`))
	if err != nil {
		t.Fatalf("expected the design doc's schema v2 example to parse: %v", err)
	}
	if f.Remote != "origin" || f.Concurrency != 0 {
		t.Fatalf("top-level = %+v", f)
	}
	if f.Defaults.Base != "auto" {
		t.Fatalf("defaults.base = %q", f.Defaults.Base)
	}
	if len(f.Defaults.Copy.Paths) != 1 || f.Defaults.Copy.Paths[0] != ".env" {
		t.Fatalf("defaults.copy.paths = %v", f.Defaults.Copy.Paths)
	}
	if len(f.Hooks.Before) != 1 || f.Hooks.Before[0].TimeoutSecs != 60 || !f.Hooks.Before[0].AllowFailure {
		t.Fatalf("hooks.before = %+v", f.Hooks.Before)
	}
	if len(f.Hooks.After) != 1 || f.Hooks.After[0].Name != "cmux-open" {
		t.Fatalf("hooks.after = %+v", f.Hooks.After)
	}
	api, ok := f.Repos["api"]
	if !ok {
		t.Fatalf("repos.api missing: %+v", f.Repos)
	}
	if api.Base != "develop" {
		t.Fatalf("repos.api.base = %q", api.Base)
	}
	if !api.Copy.Gitignored || len(api.Copy.Exclude) != 1 || api.Copy.Exclude[0] != "tmp/cache" {
		t.Fatalf("repos.api.copy = %+v", api.Copy)
	}
	backend, ok := api.Servers["backend"]
	if !ok {
		t.Fatalf("repos.api.servers.backend missing: %+v", api.Servers)
	}
	if backend.Start.Script() != "bin/rails s -p 3000" || backend.Dir != "backend" {
		t.Fatalf("backend = %+v", backend)
	}
	if backend.Setup == nil || backend.Setup.Script() != "bundle install && bin/rails db:setup" {
		t.Fatalf("backend.setup = %v", backend.Setup)
	}
	if backend.Grace().Seconds() != 10 {
		t.Fatalf("grace = %v", backend.Grace())
	}
	web, ok := f.Repos["web"]
	if !ok || web.Servers["frontend"].Start.Script() != "pnpm dev" {
		t.Fatalf("repos.web = %+v", web)
	}
	// repos_dir / worktrees_dir の "~" 展開。
	home, herr := os.UserHomeDir()
	if herr != nil {
		t.Skipf("home dir unavailable: %v", herr)
	}
	if f.ReposDir != filepath.Join(home, "repositories") {
		t.Fatalf("repos_dir tilde expansion = %q", f.ReposDir)
	}
	if f.WorktreesDir != filepath.Join(home, "worktrees") {
		t.Fatalf("worktrees_dir tilde expansion = %q", f.WorktreesDir)
	}
}

func TestHookCommandStringOrArray(t *testing.T) {
	f, err := LoadFrom(writeConfig(t, `
[[hooks.after_worktree]]
name = "single"
command = "npm install"

[[hooks.after_worktree]]
name = "many"
command = ["npm ci", "npm run build"]
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Hooks.AfterWorktree[0].Command.Script(); got != "npm install" {
		t.Fatalf("single = %q", got)
	}
	if got := f.Hooks.AfterWorktree[1].Command.Script(); got != "npm ci && npm run build" {
		t.Fatalf("many = %q", got)
	}
}

func TestOmittingHooksYieldsNone(t *testing.T) {
	f, err := LoadFrom(writeConfig(t, "remote = \"origin\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !f.Hooks.IsEmpty() {
		t.Fatalf("hooks not empty: %+v", f.Hooks)
	}
}

// (b) background キーの誘導エラー。
func TestHooksBackgroundKeyGuidesToServers(t *testing.T) {
	_, err := LoadFrom(writeConfig(t, `
[[hooks.after_worktree]]
name = "server"
command = "npm run dev"
background = true
`))
	if err == nil {
		t.Fatal("background should be rejected")
	}
	if !strings.Contains(err.Error(), "background") || !strings.Contains(err.Error(), "repos.<") ||
		!strings.Contains(err.Error(), "servers") {
		t.Fatalf("err = %q, want guidance toward [repos.<repo>.servers]", err)
	}
}

// (c) 旧スキーマ [servers.x.y] / [copy] の移行ヒント付きエラー。
func TestLegacyServersSchemaGuidesToV2(t *testing.T) {
	_, err := LoadFrom(writeConfig(t, `
[servers.rails-tutorial.backend]
start = "bin/rails server -p 3000"
`))
	if err == nil {
		t.Fatal("legacy [servers.*] should be rejected")
	}
	if !strings.Contains(err.Error(), "repos.<repo>.servers") {
		t.Fatalf("err = %q, want migration hint toward [repos.<repo>.servers.<server>]", err)
	}
}

func TestLegacyCopySchemaGuidesToV2(t *testing.T) {
	_, err := LoadFrom(writeConfig(t, `
[copy]
"*" = [".env"]
`))
	if err == nil {
		t.Fatal("legacy [copy] should be rejected")
	}
	if !strings.Contains(err.Error(), "defaults.copy") || !strings.Contains(err.Error(), "repos.<repo>.copy") {
		t.Fatalf("err = %q, want migration hint toward [defaults.copy] / [repos.<repo>.copy]", err)
	}
}

func TestUnknownServerKeyRejected(t *testing.T) {
	_, err := LoadFrom(writeConfig(t, `
[repos.app.servers.backend]
start = "x"
nope = "y"
`))
	if err == nil || (!strings.Contains(err.Error(), "nope") && !strings.Contains(err.Error(), "unknown")) {
		t.Fatalf("expected unknown-key error, got %v", err)
	}
}

// TestValidateRejectsInvalidConfigs は validate が強制する必須フィールド欠落を、
// タイミング別フックとサーバーについてまとめて検証する。
func TestValidateRejectsInvalidConfigs(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{
			name: "before フックの name 欠落",
			content: `
[[hooks.before]]
command = "echo hi"
`,
			want: "before フックに `name` がありません",
		},
		{
			name: "after_worktree フックの name 欠落",
			content: `
[[hooks.after_worktree]]
command = "echo hi"
`,
			want: "after_worktree フックに `name` がありません",
		},
		{
			name: "after フックの name 欠落",
			content: `
[[hooks.after]]
command = "echo hi"
`,
			want: "after フックに `name` がありません",
		},
		{
			name: "before フックの command 欠落",
			content: `
[[hooks.before]]
name = "noop"
`,
			want: "フック \"noop\" に `command` がありません",
		},
		{
			name: "サーバーの start 欠落",
			content: `
[repos.app.servers.backend]
dir = "backend"
`,
			want: "`start` コマンドがありません",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadFrom(writeConfig(t, tc.content))
			if err == nil {
				t.Fatalf("expected validation error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tc.want)
			}
			if !strings.Contains(err.Error(), "解析できません") {
				t.Fatalf("error = %q, want wrapping with parse-configuration prefix", err.Error())
			}
		})
	}
}

// TestValidateReportsFirstMissingStartAcrossRepos は、複数リポジトリにまたがる
// start 欠落のうち、リポジトリ名のソート順で最初に見つかったものを報告することを
// 確認する。
func TestValidateReportsFirstMissingStartAcrossRepos(t *testing.T) {
	_, err := LoadFrom(writeConfig(t, `
[repos.zebra.servers.web]
dir = "web"

[repos.alpha.servers.api]
dir = "api"
`))
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "サーバー [repos.alpha.servers.api]") {
		t.Fatalf("error = %q, want alpha reported first", err.Error())
	}
}

func TestValidValidatesCleanly(t *testing.T) {
	f, err := LoadFrom(writeConfig(t, `
[[hooks.before]]
name = "ok"
command = "echo ok"

[repos.app.servers.backend]
start = "bin/rails server"
`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(f.Hooks.Before) != 1 || f.Hooks.Before[0].Name != "ok" {
		t.Fatalf("before = %+v", f.Hooks.Before)
	}
}

// (d) "~" 展開: repos_dir / worktrees_dir が "~" または "~/..." のとき展開される。
// 絶対パス・相対パスはそのまま。
func TestTildeExpansion(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("home dir unavailable: %v", err)
	}
	f, err := LoadFrom(writeConfig(t, `
repos_dir     = "~/src"
worktrees_dir = "~"
`))
	if err != nil {
		t.Fatal(err)
	}
	if f.ReposDir != filepath.Join(home, "src") {
		t.Fatalf("repos_dir = %q", f.ReposDir)
	}
	if f.WorktreesDir != home {
		t.Fatalf("worktrees_dir = %q", f.WorktreesDir)
	}

	f, err = LoadFrom(writeConfig(t, `repos_dir = "/abs/path"`))
	if err != nil {
		t.Fatal(err)
	}
	if f.ReposDir != "/abs/path" {
		t.Fatalf("absolute path must not be touched: %q", f.ReposDir)
	}
}

// (e) 既定除外と exclude_defaults=false でのオプトアウト。
func TestCopyPlanBuiltinExcludeDefaults(t *testing.T) {
	f, err := LoadFrom(writeConfig(t, `
[repos.api.copy]
gitignored = true
`))
	if err != nil {
		t.Fatal(err)
	}
	plan := MergeCopy(f.Defaults.Copy, f.Repos["api"].Copy)
	for _, want := range []string{"node_modules", ".venv", "venv", "target", ".direnv", ".cache", ".DS_Store"} {
		found := false
		for _, ex := range plan.Exclude {
			if ex == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("built-in default %q missing from exclude: %v", want, plan.Exclude)
		}
	}
}

func TestCopyPlanExcludeDefaultsOptOut(t *testing.T) {
	f, err := LoadFrom(writeConfig(t, `
[repos.api.copy]
gitignored       = true
exclude_defaults = false
exclude          = ["custom"]
`))
	if err != nil {
		t.Fatal(err)
	}
	plan := MergeCopy(f.Defaults.Copy, f.Repos["api"].Copy)
	if len(plan.Exclude) != 1 || plan.Exclude[0] != "custom" {
		t.Fatalf("exclude_defaults=false should opt out of built-ins entirely: %v", plan.Exclude)
	}
}

// defaults.copy と repos.<name>.copy はマージされる（paths は和集合・重複排除、
// gitignored は OR、exclude は和集合）。
func TestCopyPlanMergesDefaultsAndRepo(t *testing.T) {
	f, err := LoadFrom(writeConfig(t, `
[defaults.copy]
paths = [".env"]

[repos.api.copy]
paths      = ["backend/.env"]
gitignored = true
exclude    = ["tmp/cache"]
`))
	if err != nil {
		t.Fatal(err)
	}
	plan := MergeCopy(f.Defaults.Copy, f.Repos["api"].Copy)
	if len(plan.Paths) != 2 || plan.Paths[0] != ".env" || plan.Paths[1] != "backend/.env" {
		t.Fatalf("paths = %v", plan.Paths)
	}
	if !plan.Gitignored {
		t.Fatal("gitignored should be true")
	}
	found := false
	for _, ex := range plan.Exclude {
		if ex == "tmp/cache" {
			found = true
		}
	}
	if !found {
		t.Fatalf("exclude should contain the repo-specific pattern: %v", plan.Exclude)
	}
	// リポジトリ設定の無いリポジトリは defaults.copy のみを継承する。
	other := MergeCopy(f.Defaults.Copy, f.Repos["other"].Copy)
	if len(other.Paths) != 1 || other.Paths[0] != ".env" || other.Gitignored {
		t.Fatalf("other = %+v", other)
	}
}

// TestLoadMissingDefaultFileIsEmpty は、既定パスに設定ファイルが無い場合に Load が
// 空の File を返すことを確認する（実 HOME を汚さないよう t.Setenv を使う）。
func TestLoadMissingDefaultFileIsEmpty(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	f, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f == nil {
		t.Fatal("expected non-nil empty File")
	}
	if f.ReposDir != "" || f.WorktreesDir != "" || f.Remote != "" || !f.Hooks.IsEmpty() || !f.ServersConfig().IsEmpty() {
		t.Fatalf("expected empty config, got %+v", f)
	}
}

// TestLoadReadsDefaultPathFromXDG は、$XDG_CONFIG_HOME/worktree-integrator/config.toml
// に設定を置くと Load がそれを読むことを確認する。
func TestLoadReadsDefaultPathFromXDG(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)

	dir := filepath.Join(xdg, "worktree-integrator")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.toml")
	content := `
repos_dir = "/srv/repos"
remote = "upstream"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.ReposDir != "/srv/repos" {
		t.Fatalf("repos_dir = %v", f.ReposDir)
	}
	if f.Remote != "upstream" {
		t.Fatalf("remote = %v", f.Remote)
	}
}

// TestLoadReadsDefaultPathFromHomeConfigWhenXDGUnset は、XDG_CONFIG_HOME 未設定時に
// ~/.config/worktree-integrator/config.toml が使われることを確認する。
func TestLoadReadsDefaultPathFromHomeConfigWhenXDGUnset(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	dir := filepath.Join(home, ".config", "worktree-integrator")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("remote = \"fork\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.Remote != "fork" {
		t.Fatalf("remote = %v", f.Remote)
	}
}

// TestLoadPropagatesValidationError は、デフォルトパスの設定ファイルが不正な場合に
// Load が validate のエラーを伝播することを確認する。
func TestLoadPropagatesValidationError(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)

	dir := filepath.Join(xdg, "worktree-integrator")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[[hooks.before]]\ncommand = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "before フックに `name` がありません") {
		t.Fatalf("expected validation error, got %v", err)
	}
}

// DefaultPath は os.UserConfigDir() を使わない（darwin では ~/Library/Application
// Support に解決されてしまうため）。XDG_CONFIG_HOME 経由でも HOME フォールバック経由
// でも、常に ~/.config 系のパスを指すことを固定する。
func TestDefaultPathDoesNotUseUserConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	path, ok := DefaultPath()
	if !ok {
		t.Fatal("expected ok=true")
	}
	want := filepath.Join(home, ".config", "worktree-integrator", "config.toml")
	if path != want {
		t.Fatalf("DefaultPath() = %q, want %q", path, want)
	}
}
