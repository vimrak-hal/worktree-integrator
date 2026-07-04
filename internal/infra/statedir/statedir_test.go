package statedir

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Default は XDG_STATE_HOME が設定されていれば、その配下の worktree-integrator を使う。
func TestDefaultUsesXDGStateHome(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdg)
	root, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(xdg, "worktree-integrator"); root.Dir() != want {
		t.Fatalf("Default().Dir() = %q, want %q", root.Dir(), want)
	}
}

// Default は XDG_STATE_HOME が未設定なら ~/.local/state/worktree-integrator に
// フォールバックする。
func TestDefaultFallsBackToHomeLocalState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", home)
	root, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".local", "state", "worktree-integrator"); root.Dir() != want {
		t.Fatalf("Default().Dir() = %q, want %q", root.Dir(), want)
	}
}

// Root から導出される各パスは、状態ルートの単一の配置規約を固定する。
func TestRootDerivesPaths(t *testing.T) {
	dir := t.TempDir()
	root := At(dir)
	tests := []struct{ name, got, want string }{
		{"Dir", root.Dir(), dir},
		{"ServersFile", root.ServersFile(), filepath.Join(dir, "servers.toml")},
		{"AliasesFile", root.AliasesFile(), filepath.Join(dir, "aliases.toml")},
		{"LogsDir", root.LogsDir(), filepath.Join(dir, "logs")},
		{"RepoLockPath", root.RepoLockPath("api"), filepath.Join(dir, "locks", "api.lock")},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %q, want %q", tt.name, tt.got, tt.want)
		}
	}
}

// WithRepoLock はロック保持中に fn を実行し、fn のエラーを伝播する。
func TestWithRepoLockRunsFnAndPropagatesError(t *testing.T) {
	root := At(t.TempDir())

	ran := false
	if err := root.WithRepoLock(t.Context(), "api", func() error {
		ran = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Fatal("fn should run under the lock")
	}
	if _, err := os.Stat(root.RepoLockPath("api")); err != nil {
		t.Fatalf("lock file should be created: %v", err)
	}

	sentinel := errors.New("boom")
	if err := root.WithRepoLock(t.Context(), "api", func() error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("WithRepoLock error = %v, want %v", err, sentinel)
	}
}

// 同一 repo のロックは直列化される。保持中の取得は ctx の期限で中断され、
// 解放後には再び取得できる。
func TestWithRepoLockSerializesSameRepo(t *testing.T) {
	root := At(t.TempDir())

	acquired := make(chan struct{})
	release := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = root.WithRepoLock(t.Context(), "api", func() error {
			close(acquired)
			<-release
			return nil
		})
	}()
	<-acquired

	// 保持中: 短い期限の ctx では取得できない。
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	err := root.WithRepoLock(ctx, "api", func() error {
		t.Error("fn must not run while the lock is held elsewhere")
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended WithRepoLock = %v, want context.DeadlineExceeded", err)
	}

	// 解放後: 取得できる。
	close(release)
	wg.Wait()
	if err := root.WithRepoLock(t.Context(), "api", func() error { return nil }); err != nil {
		t.Fatalf("WithRepoLock after release = %v", err)
	}
}

// 異なる repo のロックは互いにブロックしない。
func TestWithRepoLockDifferentReposAreIndependent(t *testing.T) {
	root := At(t.TempDir())

	acquired := make(chan struct{})
	release := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = root.WithRepoLock(t.Context(), "api", func() error {
			close(acquired)
			<-release
			return nil
		})
	}()
	<-acquired
	defer func() { close(release); wg.Wait() }()

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := root.WithRepoLock(ctx, "web", func() error { return nil }); err != nil {
		t.Fatalf("a different repo's lock should be free: %v", err)
	}
}

// キャンセル済みの ctx では fn を実行せず即座に失敗する。
func TestWithRepoLockRejectsCanceledContext(t *testing.T) {
	root := At(t.TempDir())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := root.WithRepoLock(ctx, "api", func() error {
		t.Error("fn must not run with a canceled context")
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("WithRepoLock(canceled) = %v, want context.Canceled", err)
	}
}
