package git_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vimrak-hal/worktree-integrator/internal/core/git"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/testutil"
)

// ctx のキャンセルは、ブロックしている fetch を中断させる。ハングするリモートは
// git-remote-ext の「ext::<command>」形式で作る: git はコマンド（ここでは sleep）を
// リモートヘルパーとして起動し、その応答を待ち続けるため、fetch は sleep の長さだけ
// ブロックする。ext トランスポートは既定で無効なので GIT_ALLOW_PROTOCOL で許可する
// （git.FetchRef は os.Environ() を引き継ぐため t.Setenv が届く）。
func TestFetchRefIsInterruptedByCancel(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "main")
	t.Setenv("GIT_ALLOW_PROTOCOL", "ext")

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		// キャンセルされなければ 30 秒ブロックする fetch。
		done <- git.FetchRef(ctx, repoPath, "ext::sleep 30", "main")
	}()

	time.Sleep(200 * time.Millisecond) // fetch がブロックに入るまで少し待つ
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("FetchRef error = %v, want context.Canceled", err)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("FetchRef was not interrupted promptly (took %v)", elapsed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("FetchRef did not return after cancel")
	}
}

// キャンセル済みの ctx では、git を実行する前に即座に失敗する。
func TestFetchRefRejectsCanceledContext(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "main")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := git.FetchRef(ctx, repoPath, "origin", "main"); !errors.Is(err, context.Canceled) {
		t.Fatalf("FetchRef(canceled) error = %v, want context.Canceled", err)
	}
}
