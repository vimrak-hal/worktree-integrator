package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vimrak-hal/worktree-integrator/internal/app/action"
	"github.com/vimrak-hal/worktree-integrator/internal/app/action/actiontest"
	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
	coreserver "github.com/vimrak-hal/worktree-integrator/internal/core/server"
	"github.com/vimrak-hal/worktree-integrator/internal/core/server/serverfake"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/childio"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/statedir"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/testutil"
)

// newApp はテスト用の App を構築する。Progress は nil（無通知）— App のメソッドが
// io.Writer にも Progress にも依存せず型付きの Result を返すことを、そのまま検証
// する形になる。
func newApp(t *testing.T) *App {
	t.Helper()
	a := New(&config.File{}, statedir.At(t.TempDir()), childio.Streams{})
	// Proc はプロセスを起動しないフェイクへ差し替える（本番の UnixProcess は使わない）。
	a.Proc = serverfake.New()
	return a
}

// alias 系メソッドの往復: set → list → remove が Result / 戻り値で観測できる。
func TestAliasMethodsRoundTrip(t *testing.T) {
	a := newApp(t)

	stored, err := a.AliasSet(t.Context(), actiontest.MustName(t, "feat-a"), "  ABC-123: title \n2行目は落ちる")
	if err != nil {
		t.Fatal(err)
	}
	// ラベルは最初の 1 行にトリムされる（正規化後の値が返る）。
	if stored != "ABC-123: title" {
		t.Fatalf("stored = %q", stored)
	}

	res, err := a.AliasList(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if res.Aliases["feat-a"] != "ABC-123: title" {
		t.Fatalf("aliases = %+v", res.Aliases)
	}
	if names := res.SortedNames(); len(names) != 1 || names[0] != "feat-a" {
		t.Fatalf("sorted names = %v", names)
	}

	existed, err := a.AliasRemove(t.Context(), actiontest.MustName(t, "feat-a"))
	if err != nil || !existed {
		t.Fatalf("remove = %v, %v", existed, err)
	}
	res, err = a.AliasList(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// 空でも Aliases は非 nil（JSON / structuredContent で null にならない）。
	if res.Aliases == nil || len(res.Aliases) != 0 {
		t.Fatalf("aliases = %#v", res.Aliases)
	}
}

// ListRepos は探索結果を型付きの Result で返す。
func TestListReposReturnsTypedResult(t *testing.T) {
	reposDir := t.TempDir()
	testutil.CloneWithBranchNamed(t, reposDir, "main", "repo-a")
	t.Setenv("WT_REPOS_DIR", reposDir)

	a := newApp(t)
	res, err := a.ListRepos(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if res.ReposDir != reposDir {
		t.Fatalf("ReposDir = %q", res.ReposDir)
	}
	if len(res.Repos) != 1 || res.Repos[0].Name != "repo-a" || res.Repos[0].Path == "" {
		t.Fatalf("repos = %+v", res.Repos)
	}
}

// 旧形式（v1）の状態ファイルは最初のサーバー操作で .bak へ退避され、その事実が
// Result の LegacyBackup として返る（表示層が警告を描画する）。
func TestLegacyStateBackupSurfacesInResult(t *testing.T) {
	a := newApp(t)
	stateFile := a.Root.ServersFile()
	legacy := "version = 1\n\n[repos.app]\nactive = \"feat-a\"\n\n[repos.app.servers.backend]\ninitialized = [\"feat-a\"]\n"
	if err := os.MkdirAll(filepath.Dir(stateFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateFile, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := a.ServerStatus(t.Context(), action.ServerCommand{
		ReposDir: t.TempDir(), WorktreesDir: t.TempDir(), Servers: coreserver.Config{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.LegacyBackup == "" || !strings.HasSuffix(res.LegacyBackup, ".bak") {
		t.Fatalf("LegacyBackup = %q", res.LegacyBackup)
	}
	if _, err := os.Stat(res.LegacyBackup); err != nil {
		t.Fatalf(".bak should exist: %v", err)
	}
}
