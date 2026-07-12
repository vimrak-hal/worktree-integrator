package tree

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/vimrak-hal/worktree-integrator/internal/app/action"
	corealias "github.com/vimrak-hal/worktree-integrator/internal/core/alias"
	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
	coreserver "github.com/vimrak-hal/worktree-integrator/internal/core/server"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/childio"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/statedir"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/testutil"
)

// newDeps はテスト用の依存束を構築する。状態ルートは一時ディレクトリ、子プロセスの
// 出力は破棄される。
func newDeps(t *testing.T, proc coreserver.ProcessControl, cfg *config.File, reposDir, worktreesDir string) Deps {
	t.Helper()
	root := statedir.At(t.TempDir())
	return Deps{
		Proc:         proc,
		Store:        coreserver.NewStateStore(root),
		Aliases:      corealias.NewStore(root),
		Root:         root,
		ChildIO:      childio.Streams{Stdout: io.Discard, Stderr: io.Discard},
		Config:       cfg,
		ReposDir:     reposDir,
		WorktreesDir: worktreesDir,
	}
}

func mustName(t *testing.T, raw string) action.Name {
	t.Helper()
	n, err := action.ParseName(raw)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// addWorktree は repoPath から target へ branch の連結ワークツリーを作る。
func addWorktree(t *testing.T, repoPath, branch, target string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	testutil.Git(t, repoPath, "worktree", "add", "-b", branch, target, "HEAD")
}
