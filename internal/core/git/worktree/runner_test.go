package worktree

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vimrak-hal/worktree-integrator/internal/infra/parallel"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/testutil"
)

// Concurrency は明示された値をリポジトリ数で頭打ちにし、0（自動）以外は最低 1 を
// 保証する（旧 EffectiveConcurrency の *int を 0=自動 の素の int に置き換えた仕様）。
func TestConcurrencyRequestedCappedAtRepoCount(t *testing.T) {
	cases := []struct {
		requested int
		repoCount int
		want      int
	}{
		{8, 3, 3},
		{2, 5, 2},
		{4, 0, 1},
	}
	for _, c := range cases {
		if got := Concurrency(c.requested, c.repoCount); got != c.want {
			t.Errorf("Concurrency(%d, %d) = %d, want %d", c.requested, c.repoCount, got, c.want)
		}
	}
}

func TestAutomaticConcurrencyTracksRepoCount(t *testing.T) {
	cap := parallel.AutoLimit()
	if cap < 4 || cap > 16 {
		t.Fatalf("cap out of band: %d", cap)
	}
	if got := Concurrency(0, 1); got != 1 {
		t.Errorf("0,1 = %d", got)
	}
	if got := Concurrency(0, 1000); got != cap {
		t.Errorf("0,1000 = %d, want %d", got, cap)
	}
}

// recordingReporter は進捗行を並行安全な方法で収集する。
type recordingReporter struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (r *recordingReporter) Update(repo string, _ Progress) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf.WriteString(repo)
}
func (r *recordingReporter) Event(string, Note) {}

func TestRunsAllRequestsAndReportsProgress(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "wt")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}

	var reqs []Request
	for _, name := range []string{"alpha", "beta", "gamma"} {
		repoPath := testutil.CloneWithBranch(t, tmp, "main")
		reqs = append(reqs, Request{
			RepoName: name, RepoPath: repoPath, WorktreeName: "feature",
			Target: filepath.Join(root, name), Remote: "origin",
		})
	}

	reporter := &recordingReporter{}
	results := Run(t.Context(), reqs, 2, reporter)
	if len(results) != 3 {
		t.Fatalf("results = %d", len(results))
	}
	for _, r := range results {
		if r.Status != StatusCreated {
			t.Fatalf("repo %s status %v stage %d err %v", r.Repo, r.Status, r.Stage, r.Err)
		}
	}
	progress := reporter.buf.String()
	for _, name := range []string{"alpha", "beta", "gamma"} {
		if !strings.Contains(progress, name) {
			t.Errorf("missing progress for %s", name)
		}
	}
}

// キャンセル済みの ctx では新規に着手せず、全リポジトリが「Skipped（キャンセル）」の
// Outcome として集約される。git には一切触れないため、RepoPath は実在しなくてよい。
func TestRunSkipsAllRequestsWhenAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	reqs := []Request{
		{RepoName: "alpha", RepoPath: "/nonexistent/alpha", WorktreeName: "feature", Target: "/nonexistent/wt/alpha", Remote: "origin"},
		{RepoName: "beta", RepoPath: "/nonexistent/beta", WorktreeName: "feature", Target: "/nonexistent/wt/beta", Remote: "origin"},
	}
	reporter := &recordingReporter{}
	results := Run(ctx, reqs, 2, reporter)
	if len(results) != 2 {
		t.Fatalf("results = %d", len(results))
	}
	for _, r := range results {
		if r.Status != StatusSkipped {
			t.Fatalf("repo %s status = %v, want StatusSkipped", r.Repo, r.Status)
		}
		if r.Stage != StageCanceled {
			t.Fatalf("repo %s stage = %d, want StageCanceled", r.Repo, r.Stage)
		}
	}
	// 終端（スキップを含む）は Reporter に報告されない — Outcome に一本化されている。
	if progress := reporter.buf.String(); progress != "" {
		t.Errorf("terminal states must not be reported as progress: %q", progress)
	}
}
