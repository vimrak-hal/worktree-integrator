package hooks

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vimrak-hal/worktree-integrator/internal/core/cmdspec"
	"github.com/vimrak-hal/worktree-integrator/internal/core/wtenv"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/childio"
)

func discard() childio.Streams {
	return childio.Streams{Stdin: nil, Stdout: io.Discard, Stderr: io.Discard}
}

func runCtx(root string) *wtenv.RunContext {
	return &wtenv.RunContext{
		WorktreeName: "feature-x",
		ReposDir:     "/repos",
		WorktreesDir: "/worktrees",
		Root:         root,
	}
}

func hook(name, command string) Hook {
	return Hook{Name: name, Command: cmdspec.FromString(command)}
}

func TestSuccessfulHookReportsSuccess(t *testing.T) {
	out := Run(t.Context(), []Hook{hook("ok", "true")}, runCtx(t.TempDir()), discard())
	if len(out) != 1 || out[0].Status != StatusSucceeded || out[0].IsFatal() {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestFailingHookIsFatalByDefault(t *testing.T) {
	out := Run(t.Context(), []Hook{hook("boom", "exit 3")}, runCtx(t.TempDir()), discard())
	if out[0].Status != StatusFailed || !out[0].IsFatal() {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestAllowFailureDowngradesToWarning(t *testing.T) {
	h := hook("soft", "exit 1")
	h.AllowFailure = true
	out := Run(t.Context(), []Hook{h}, runCtx(t.TempDir()), discard())
	if out[0].Status != StatusWarned || out[0].IsFatal() {
		t.Fatalf("outcome = %+v", out)
	}
}

// timeout_secs を超えたフックは ctx ベースで強制終了され、AllowFailure に従って
// 失敗または警告として報告される（(k) の検証要件）。
func TestTimeoutKillsHookAndReportsFailed(t *testing.T) {
	h := hook("slow", "sleep 30")
	h.TimeoutSecs = 1

	start := time.Now()
	out := Run(t.Context(), []Hook{h}, runCtx(t.TempDir()), discard())
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("hook was not killed promptly by its timeout (took %v)", elapsed)
	}
	if out[0].Status != StatusFailed {
		t.Fatalf("outcome = %+v, want StatusFailed", out[0])
	}
	if !strings.Contains(out[0].Detail, "タイムアウト") {
		t.Fatalf("detail = %q, want it to mention the timeout", out[0].Detail)
	}
}

// AllowFailure を立てたフックがタイムアウトした場合は警告に格下げされる。
func TestTimeoutHonorsAllowFailure(t *testing.T) {
	h := hook("slow", "sleep 30")
	h.TimeoutSecs = 1
	h.AllowFailure = true

	out := Run(t.Context(), []Hook{h}, runCtx(t.TempDir()), discard())
	if out[0].Status != StatusWarned || out[0].IsFatal() {
		t.Fatalf("outcome = %+v, want StatusWarned", out[0])
	}
}

// timeout_secs 省略（0）は無制限であり、それより短く終わるコマンドは通常どおり
// 成功として報告される。
func TestZeroTimeoutIsUnlimited(t *testing.T) {
	h := hook("quick", "true")
	if h.TimeoutSecs != 0 {
		t.Fatalf("TimeoutSecs = %d, want 0 by default", h.TimeoutSecs)
	}
	out := Run(t.Context(), []Hook{h}, runCtx(t.TempDir()), discard())
	if out[0].Status != StatusSucceeded {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestHookReceivesWTEnvironment(t *testing.T) {
	tmp := t.TempDir()
	marker := filepath.Join(tmp, "env.txt")
	cmd := fmt.Sprintf("printf '%%s %%s' \"$WT_WORKTREE_NAME\" \"$WT_ROOT\" > %s", marker)
	out := Run(t.Context(), []Hook{hook("env", cmd)}, runCtx(tmp), discard())
	if out[0].Status != StatusSucceeded {
		t.Fatalf("outcome = %+v", out)
	}
	data, _ := os.ReadFile(marker)
	if string(data) != fmt.Sprintf("feature-x %s", tmp) {
		t.Fatalf("env = %q", data)
	}
}

func TestAfterWorktreeHookRunsInsideWorktree(t *testing.T) {
	tmp := t.TempDir()
	worktree := filepath.Join(tmp, "repo-a")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	repos := []wtenv.RepoContext{{RepoName: "repo-a", RepoPath: "/repos/repo-a", WorktreePath: worktree}}
	out := RunWorktree(t.Context(),
		[]Hook{hook("in-wt", "printf '%s' \"$WT_REPO_NAME\" > created.txt")},
		runCtx(tmp), repos, discard())
	if out[0].Status != StatusSucceeded {
		t.Fatalf("outcome = %+v", out)
	}
	data, _ := os.ReadFile(filepath.Join(worktree, "created.txt"))
	if string(data) != "repo-a" {
		t.Fatalf("contents = %q", data)
	}
}

func TestHooksForATimingRunInParallel(t *testing.T) {
	tmp := t.TempDir()
	a := filepath.Join(tmp, "a")
	b := filepath.Join(tmp, "b")
	waitFor := func(p string) string {
		return fmt.Sprintf("until [ -e %s ]; do sleep 0.01; done", p)
	}
	first := hook("first", fmt.Sprintf("touch %s; %s", a, waitFor(b)))
	second := hook("second", fmt.Sprintf("touch %s; %s", b, waitFor(a)))
	out := Run(t.Context(), []Hook{first, second}, runCtx(tmp), discard())
	for _, o := range out {
		if o.Status != StatusSucceeded {
			t.Fatalf("outcome = %+v", out)
		}
	}
}

func TestMultipleCommandsRunInSequence(t *testing.T) {
	tmp := t.TempDir()
	marker := filepath.Join(tmp, "seq.txt")
	h := Hook{Name: "seq"}
	_ = h.Command.UnmarshalTOML([]any{
		fmt.Sprintf("printf 'a' > %s", marker),
		fmt.Sprintf("printf 'b' >> %s", marker),
	})
	out := Run(t.Context(), []Hook{h}, runCtx(tmp), discard())
	if out[0].Status != StatusSucceeded {
		t.Fatalf("outcome = %+v", out)
	}
	if data, _ := os.ReadFile(marker); string(data) != "ab" {
		t.Fatalf("contents = %q", data)
	}
}

func TestMultipleCommandsStopAtFirstFailure(t *testing.T) {
	tmp := t.TempDir()
	marker := filepath.Join(tmp, "after-fail.txt")
	h := Hook{Name: "seq"}
	_ = h.Command.UnmarshalTOML([]any{"exit 1", fmt.Sprintf("printf 'reached' > %s", marker)})
	out := Run(t.Context(), []Hook{h}, runCtx(tmp), discard())
	if out[0].Status != StatusFailed {
		t.Fatalf("outcome = %+v", out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("second command ran after the first failed")
	}
}

// syncBuffer は複数 goroutine から安全に書き込める bytes.Buffer ラッパ（並列実行
// される複数フックの出力先として共有するため）。
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// 出力は hook 名で "[name] " プレフィックスされた行として届く。並列実行される
// 複数フックの出力が行の途中で混ざらないようにするための仕組みである。
func TestOutputIsTaggedWithHookName(t *testing.T) {
	var buf syncBuffer
	streams := childio.Streams{Stdout: &buf, Stderr: &buf}
	out := Run(t.Context(), []Hook{hook("greeter", "echo hello")}, runCtx(t.TempDir()), streams)
	if out[0].Status != StatusSucceeded {
		t.Fatalf("outcome = %+v", out)
	}
	if got := buf.String(); got != "[greeter] hello\n" {
		t.Fatalf("output = %q, want tagged with [greeter]", got)
	}
}

// 並列に実行される複数フックの出力は、それぞれ自分のタグの行としてのみ現れ、
// 他方の行に混ざらない。
func TestParallelHookOutputDoesNotInterleave(t *testing.T) {
	var buf syncBuffer
	streams := childio.Streams{Stdout: &buf, Stderr: &buf}
	out := Run(t.Context(), []Hook{
		hook("first", "echo one"),
		hook("second", "echo two"),
	}, runCtx(t.TempDir()), streams)
	for _, o := range out {
		if o.Status != StatusSucceeded {
			t.Fatalf("outcome = %+v", out)
		}
	}
	got := buf.String()
	if !strings.Contains(got, "[first] one\n") || !strings.Contains(got, "[second] two\n") {
		t.Fatalf("output = %q, want both tagged lines present intact", got)
	}
}

func TestEmptyHookListDoesNothing(t *testing.T) {
	if out := Run(t.Context(), nil, runCtx(t.TempDir()), discard()); len(out) != 0 {
		t.Fatalf("outcome = %+v", out)
	}
}

// ctx のキャンセルは実行中のフォアグラウンドフックを中断させ、その結果は
// キャンセルを明示した失敗として報告される。sleep 30 を丸ごと待たずに返ることで
// 「中断された」ことを確かめる。
func TestHookIsInterruptedByCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	out := Run(ctx, []Hook{hook("slow", "sleep 30")}, runCtx(t.TempDir()), discard())
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("hook was not interrupted promptly (took %v)", elapsed)
	}
	if out[0].Status != StatusFailed {
		t.Fatalf("outcome = %+v, want StatusFailed", out[0])
	}
	if !strings.Contains(out[0].Detail, "canceled") {
		t.Fatalf("detail = %q, want it to mention the cancellation", out[0].Detail)
	}
}
