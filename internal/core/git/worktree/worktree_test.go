package worktree

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/vimrak-hal/worktree-integrator/internal/core/git"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/testutil"
)

type noopReporter struct{}

func (noopReporter) Update(string, Progress) {}
func (noopReporter) Event(string, Note)      {}

// noteRecorder は報告された Note を蓄積する Reporter。fetch degrade のような
// 途中経過イベントが実際に発行されたことをテストで確認するために使う。
type noteRecorder struct {
	mu    sync.Mutex
	notes []Note
}

func (r *noteRecorder) Update(string, Progress) {}

func (r *noteRecorder) Event(_ string, n Note) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notes = append(r.notes, n)
}

func (r *noteRecorder) hasNote(kind NoteKind) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, n := range r.notes {
		if n.Kind == kind {
			return true
		}
	}
	return false
}

func request(repoPath, name, target string) Request {
	return Request{
		RepoName:     "repo",
		RepoPath:     repoPath,
		WorktreeName: name,
		Target:       target,
		Remote:       "origin",
	}
}

func mkParent(t *testing.T, target string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestCreatesWorktreeOnNewBranch(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "main")
	target := filepath.Join(tmp, "wt", "repo")
	mkParent(t, target)

	out := Process(t.Context(), request(repoPath, "feature-x", target), noopReporter{})
	if out.Status != StatusCreated {
		t.Fatalf("status = %v, stage=%d err=%v", out.Status, out.Stage, out.Err)
	}
	if _, err := os.Stat(filepath.Join(target, ".git")); err != nil {
		t.Fatal("worktree gitfile missing")
	}
	if _, err := os.Stat(filepath.Join(target, "README.md")); err != nil {
		t.Fatal("checkout content missing")
	}
	if ok, _ := git.LocalBranchExists(t.Context(), repoPath, "feature-x"); !ok {
		t.Fatal("branch feature-x missing")
	}
}

func TestCreatesWorktreeWithSlashedName(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "main")
	target := filepath.Join(tmp, "wt", "feat", "sub-x", "repo")
	mkParent(t, target)

	out := Process(t.Context(), request(repoPath, "feat/sub-x", target), noopReporter{})
	if out.Status != StatusCreated {
		t.Fatalf("status = %v, stage=%d err=%v", out.Status, out.Stage, out.Err)
	}
	if _, err := os.Stat(filepath.Join(target, ".git")); err != nil {
		t.Fatal("worktree gitfile missing")
	}
	if ok, _ := git.LocalBranchExists(t.Context(), repoPath, "feat/sub-x"); !ok {
		t.Fatal("slashed branch feat/sub-x missing")
	}
}

func TestFallsBackToMaster(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "master")
	// symbolic-ref を消し、main/master の存在確認によるフォールバックを強制する
	// （clone は通常 origin/HEAD の symbolic-ref を設定するため、それがあると
	// この経路を通らず素通りしてしまう）。
	testutil.Git(t, repoPath, "symbolic-ref", "-d", "refs/remotes/origin/HEAD")
	target := filepath.Join(tmp, "wt", "repo")
	mkParent(t, target)

	out := Process(t.Context(), request(repoPath, "feature-x", target), noopReporter{})
	if out.Status != StatusCreated {
		t.Fatalf("status = %v, stage=%d err=%v", out.Status, out.Stage, out.Err)
	}
}

// base 省略時（Request.Base==""）は AutoBase 扱いとなり、symbolic-ref → main → master
// の順でデフォルトブランチを解決する。symbolic-ref が無く、main も master も無い
// リモートでは StageResolve で失敗する。
func TestFailsWhenNoMainOrMaster(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "develop")
	testutil.Git(t, repoPath, "symbolic-ref", "-d", "refs/remotes/origin/HEAD")
	target := filepath.Join(tmp, "wt", "repo")
	mkParent(t, target)

	out := Process(t.Context(), request(repoPath, "feature-x", target), noopReporter{})
	if out.Status != StatusFailed {
		t.Fatalf("status = %v", out.Status)
	}
	if out.Stage != StageResolve {
		t.Fatalf("stage = %d, want StageResolve", out.Stage)
	}
}

// symbolic-ref が生きていれば、main/master のどちらでもない名前のデフォルトブランチ
// （例: develop）でも auto 解決で正しく使われる。
func TestAutoResolvesNonStandardDefaultBranchViaSymbolicRef(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "develop")
	target := filepath.Join(tmp, "wt", "repo")
	mkParent(t, target)

	out := Process(t.Context(), request(repoPath, "feature-x", target), noopReporter{})
	if out.Status != StatusCreated {
		t.Fatalf("status = %v, stage=%d err=%v", out.Status, out.Stage, out.Err)
	}
}

// (h) fetch 失敗時の degrade: 既存の追跡ブランチ（refs/remotes/<remote>/<branch>）が
// 既にあれば、fetch 自体が失敗しても NoteFetchDegraded を発して作成を続行する
// （オフライン degrade）。リモート URL を存在しないパスへ差し替えることで fetch を
// 確実に失敗させつつ、クローン時点の refs/remotes/origin/main は手つかずのまま残す。
func TestFetchFailureDegradesToExistingRef(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "main")
	target := filepath.Join(tmp, "wt", "repo")
	mkParent(t, target)

	testutil.Git(t, repoPath, "remote", "set-url", "origin", filepath.Join(tmp, "does-not-exist"))

	reporter := &noteRecorder{}
	out := Process(t.Context(), request(repoPath, "feature-x", target), reporter)
	if out.Status != StatusCreated {
		t.Fatalf("status = %v, stage=%d err=%v", out.Status, out.Stage, out.Err)
	}
	if !reporter.hasNote(NoteFetchDegraded) {
		t.Fatalf("expected NoteFetchDegraded to be reported, got %+v", reporter.notes)
	}
}

// (h) fetch 失敗時の degrade: 既存の追跡ブランチが無ければオフライン degrade は
// 効かず、StageFetch で失敗する。
func TestFetchFailureWithNoExistingRefFails(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "main")
	target := filepath.Join(tmp, "wt", "repo")
	mkParent(t, target)

	testutil.Git(t, repoPath, "remote", "set-url", "origin", filepath.Join(tmp, "does-not-exist"))

	req := request(repoPath, "feature-x", target)
	// このブランチは一度もフェッチされておらず、refs/remotes/origin/totally-new-branch
	// は存在しない。
	req.Base = "totally-new-branch"
	out := Process(t.Context(), req, noopReporter{})
	if out.Status != StatusFailed {
		t.Fatalf("status = %v", out.Status)
	}
	if out.Stage != StageFetch {
		t.Fatalf("stage = %d, want StageFetch", out.Stage)
	}
}

func TestEmptyDestinationIsReused(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "main")
	target := filepath.Join(tmp, "wt", "repo")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}

	out := Process(t.Context(), request(repoPath, "feature-x", target), noopReporter{})
	if out.Status != StatusCreated {
		t.Fatalf("status = %v, stage=%d err=%v", out.Status, out.Stage, out.Err)
	}
	if _, err := os.Stat(filepath.Join(target, ".git")); err != nil {
		t.Fatal("worktree gitfile missing")
	}
}

func TestExistingWorktreeIsSkipped(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "main")
	target := filepath.Join(tmp, "wt", "repo")
	mkParent(t, target)

	first := Process(t.Context(), request(repoPath, "feature-x", target), noopReporter{})
	if first.Status != StatusCreated {
		t.Fatalf("first status = %v, stage=%d err=%v", first.Status, first.Stage, first.Err)
	}
	second := Process(t.Context(), request(repoPath, "feature-x", target), noopReporter{})
	if second.Status != StatusSkipped {
		t.Fatalf("second status = %v", second.Status)
	}
	if second.Stage != StageDestination {
		t.Fatalf("stage = %d, want StageDestination", second.Stage)
	}
}

func TestOccupiedDestinationFailsWithoutClobbering(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "main")
	target := filepath.Join(tmp, "wt", "repo")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "notes.txt"), []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := Process(t.Context(), request(repoPath, "feature-x", target), noopReporter{})
	if out.Status != StatusFailed {
		t.Fatalf("status = %v", out.Status)
	}
	if out.Stage != StageDestination || out.Err == nil {
		t.Fatalf("stage = %d err = %v, want StageDestination with an error", out.Stage, out.Err)
	}
	data, _ := os.ReadFile(filepath.Join(target, "notes.txt"))
	if string(data) != "keep me" {
		t.Fatal("foreign content must be left intact")
	}
}

func TestFailsWhenBranchExists(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "main")
	target := filepath.Join(tmp, "wt", "repo")
	mkParent(t, target)
	testutil.Git(t, repoPath, "branch", "dup")

	out := Process(t.Context(), request(repoPath, "dup", target), noopReporter{})
	if out.Status != StatusFailed {
		t.Fatalf("status = %v", out.Status)
	}
	if out.Stage != StageBranchCheck || out.Err == nil {
		t.Fatalf("stage = %d err = %v, want StageBranchCheck with an error", out.Stage, out.Err)
	}
}

func TestRecreatesAfterOrphanedWorktreeMetadata(t *testing.T) {
	tmp := t.TempDir()
	repoPath := testutil.CloneWithBranch(t, tmp, "main")

	// 連結ワークツリーのエントリを孤立させる。そのディレクトリを手作業で削除し、
	// 古い .git/worktrees メタデータだけが残る状態を作る。
	orphan := filepath.Join(tmp, "oldwt", "repo")
	if err := os.MkdirAll(filepath.Dir(orphan), 0o755); err != nil {
		t.Fatal(err)
	}
	testutil.Git(t, repoPath, "worktree", "add", "-b", "throwaway", orphan)
	if err := os.RemoveAll(orphan); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(tmp, "wt", "repo")
	mkParent(t, target)
	out := Process(t.Context(), request(repoPath, "feature-x", target), noopReporter{})
	if out.Status != StatusCreated {
		t.Fatalf("status = %v, stage=%d err=%v", out.Status, out.Stage, out.Err)
	}
	if _, err := os.Stat(filepath.Join(target, ".git")); err != nil {
		t.Fatal("worktree gitfile missing")
	}
}

// 追加ファイルのコピーは worktree.Process から分離され、app/create の post-create
// ステップ（copyExtras）になった。コピーのテストは app/create と infra/fscopy に
// 移設されている。
