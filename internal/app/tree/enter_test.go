package tree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vimrak-hal/worktree-integrator/internal/core/cmdspec"
	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
	"github.com/vimrak-hal/worktree-integrator/internal/core/hooks"
	"github.com/vimrak-hal/worktree-integrator/internal/core/server/serverfake"
)

// hooksWithMarkers は 3 タイミングすべてにマーカーを touch するフックを持つ設定を
// 返す。
func hooksWithMarkers(dir string) hooks.Config {
	touch := func(name string) cmdspec.Commands {
		return cmdspec.FromString("touch \"" + filepath.Join(dir, name) + "\"")
	}
	return hooks.Config{
		Before:        []hooks.Hook{{Name: "pre", Command: touch("before-ran")}},
		AfterWorktree: []hooks.Hook{{Name: "per", Command: touch("after-worktree-ran")}},
		After:         []hooks.Hook{{Name: "nav", Command: touch("after-ran")}},
	}
}

// enter は after フックだけを実行する（before / after_worktree は実行されない）。
func TestEnterRunsOnlyAfterHooks(t *testing.T) {
	worktreesDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(worktreesDir, "feat-x"), 0o755); err != nil {
		t.Fatal(err)
	}
	markers := t.TempDir()
	cfg := &config.File{Hooks: hooksWithMarkers(markers)}
	d := newDeps(t, serverfake.New(), cfg, t.TempDir(), worktreesDir)

	res, err := Enter(t.Context(), d, mustName(t, "feat-x"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Worktree != "feat-x" || res.Root != filepath.Join(worktreesDir, "feat-x") {
		t.Fatalf("res = %+v", res)
	}
	if len(res.Hooks) != 1 || res.Hooks[0].Timing != "after" || res.Hooks[0].Name != "nav" {
		t.Fatalf("hooks = %+v", res.Hooks)
	}
	if _, err := os.Stat(filepath.Join(markers, "after-ran")); err != nil {
		t.Fatal("after hook should have run")
	}
	for _, name := range []string{"before-ran", "after-worktree-ran"} {
		if _, err := os.Stat(filepath.Join(markers, name)); err == nil {
			t.Fatalf("%s must not run on enter", name)
		}
	}
}

// ルートが無ければエラー（フックも実行されない）。
func TestEnterMissingWorktreeIsError(t *testing.T) {
	markers := t.TempDir()
	cfg := &config.File{Hooks: hooksWithMarkers(markers)}
	d := newDeps(t, serverfake.New(), cfg, t.TempDir(), t.TempDir())

	res, err := Enter(t.Context(), d, mustName(t, "no-such"))
	if err == nil || !strings.Contains(err.Error(), `worktree "no-such" がありません`) {
		t.Fatalf("err = %v", err)
	}
	if res != nil {
		t.Fatalf("res = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(markers, "after-ran")); err == nil {
		t.Fatal("after hook must not run when the worktree is missing")
	}
}

// after フックの失敗はエラーとして返り、結果にはフックの結末が残る。
func TestEnterAfterHookFailureIsError(t *testing.T) {
	worktreesDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(worktreesDir, "feat-x"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.File{Hooks: hooks.Config{
		After: []hooks.Hook{{Name: "boom", Command: cmdspec.FromString("false")}},
	}}
	d := newDeps(t, serverfake.New(), cfg, t.TempDir(), worktreesDir)

	res, err := Enter(t.Context(), d, mustName(t, "feat-x"))
	if err == nil {
		t.Fatal("failing after hook should surface as an error")
	}
	if res == nil || len(res.Hooks) != 1 || res.Hooks[0].Status != "failed" {
		t.Fatalf("res = %+v", res)
	}
}
