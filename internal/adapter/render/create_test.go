package render

import (
	"bytes"
	"strings"
	"testing"

	"github.com/vimrak-hal/worktree-integrator/internal/app/create"
)

func TestCreateSummaryGroupsRepos(t *testing.T) {
	res := &create.Result{
		Worktree:    "feat",
		Root:        "/wt/feat",
		ReposDir:    "/repos",
		Disposition: create.DispositionCreated,
		Discovered:  3,
		Repos: []create.RepoOutcome{
			{Repo: "a", Status: create.RepoCreated},
			{Repo: "b", Status: create.RepoSkipped, Stage: "destination"},
			{Repo: "c", Status: create.RepoFailed, Stage: "fetch", Error: "boom"},
		},
		Created: 1, Skipped: 1, Failed: 1,
	}
	var buf bytes.Buffer
	Create(&buf, res)
	out := buf.String()
	if !strings.Contains(out, "サマリ: 1 作成, 1 スキップ, 1 失敗") {
		t.Fatalf("summary line missing: %q", out)
	}
	if !strings.Contains(out, "✓ a") ||
		!strings.Contains(out, "- b (スキップ: worktree は既に存在します)") ||
		!strings.Contains(out, "✗ c (fetch: boom)") {
		t.Fatalf("group lines missing: %q", out)
	}
}

// キャンセルによる未着手スキップ（stage=canceled）はキャンセルと表示される。
func TestCreateCanceledSkipLabel(t *testing.T) {
	var buf bytes.Buffer
	Create(&buf, &create.Result{
		Disposition: create.DispositionCreated,
		Repos:       []create.RepoOutcome{{Repo: "a", Status: create.RepoSkipped, Stage: "canceled"}},
		Skipped:     1,
	})
	if !strings.Contains(buf.String(), "- a (スキップ: キャンセル)") {
		t.Fatalf("canceled skip label missing: %q", buf.String())
	}
}

// コピーの部分失敗は Created の行に件数付きで表示される。
func TestCreateCopyPartialFailureAnnotated(t *testing.T) {
	var buf bytes.Buffer
	Create(&buf, &create.Result{
		Disposition: create.DispositionCreated,
		Repos: []create.RepoOutcome{{
			Repo: "a", Status: create.RepoCreated, Stage: "copy",
			Copy: &create.CopyReport{
				Copied:   []string{".env"},
				Failures: []create.CopyFailure{{Path: "secret", Error: "permission denied"}},
			},
		}},
		Created: 1,
	})
	if !strings.Contains(buf.String(), "✓ a（コピー失敗 1 件）") {
		t.Fatalf("copy failure annotation missing: %q", buf.String())
	}
}

// 失敗行でエラーが空なら「不明なエラー」を表示する。
func TestCreateFailedWithoutErrorShowsUnknown(t *testing.T) {
	var buf bytes.Buffer
	Create(&buf, &create.Result{
		Disposition: create.DispositionCreated,
		Repos:       []create.RepoOutcome{{Repo: "a", Status: create.RepoFailed, Stage: "add"}},
		Failed:      1,
	})
	if !strings.Contains(buf.String(), "不明なエラー") {
		t.Fatalf("expected unknown-error placeholder: %q", buf.String())
	}
}

// Disposition ごとの案内文言。
func TestCreateDispositions(t *testing.T) {
	t.Run("nothing to add", func(t *testing.T) {
		// 全リポジトリ作成済み（差分作成の候補が無い）。after フックの結果も併記される。
		var buf bytes.Buffer
		Create(&buf, &create.Result{
			Worktree: "feat", Root: "/wt/feat", Disposition: create.DispositionNothingToAdd,
			Hooks: []create.HookOutcome{{Timing: "after", Name: "nav", Status: create.HookSucceeded}},
		})
		if !strings.Contains(buf.String(), "追加するリポジトリはありません") {
			t.Fatalf("out = %q", buf.String())
		}
		if !strings.Contains(buf.String(), "✓ hook nav 完了") {
			t.Fatalf("after hook line missing: %q", buf.String())
		}
	})
	t.Run("no repositories", func(t *testing.T) {
		var buf bytes.Buffer
		Create(&buf, &create.Result{ReposDir: "/repos", Disposition: create.DispositionNoRepos})
		if !strings.Contains(buf.String(), "リポジトリが見つかりません（/repos）") {
			t.Fatalf("out = %q", buf.String())
		}
	})
	t.Run("nothing selected", func(t *testing.T) {
		var buf bytes.Buffer
		Create(&buf, &create.Result{Disposition: create.DispositionNothingSelected})
		if !strings.Contains(buf.String(), "選択されませんでした") {
			t.Fatalf("out = %q", buf.String())
		}
	})
}

// フックはタイミングごとにヘッダー付きでグループ描画され、3 状態がそれぞれ正しい
// 記号と文言で出力される（旧 StatusStarted は background 廃止に伴い削除された）。
func TestCreateHookGroups(t *testing.T) {
	res := &create.Result{
		Disposition: create.DispositionCreated,
		Created:     2,
		Hooks: []create.HookOutcome{
			{Timing: "before", Name: "ok", Status: create.HookSucceeded},
			{Timing: "after_worktree", Name: "warn", Status: create.HookWarned, Detail: "exit 1"},
			{Timing: "after", Name: "boom", Status: create.HookFailed, Detail: "no such file"},
		},
	}
	var buf bytes.Buffer
	Create(&buf, res)
	out := buf.String()
	for _, want := range []string{
		"before フックを 1 件実行:",
		"  ✓ hook ok 完了\n",
		"after_worktree フックを 2 リポジトリで実行:",
		"  ! hook warn 失敗（許容）: exit 1\n",
		"after フックを 1 件実行:",
		"  ✗ hook boom 失敗: no such file\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
}

// フックが無いタイミングはヘッダーも出さない。
func TestCreateNoHooksWritesNoHeaders(t *testing.T) {
	var buf bytes.Buffer
	Create(&buf, &create.Result{Disposition: create.DispositionCreated})
	if strings.Contains(buf.String(), "フック") {
		t.Fatalf("out = %q", buf.String())
	}
}

// setup 記録の無効化に失敗した場合の警告行。
func TestCreateSetupInvalidateWarning(t *testing.T) {
	var buf bytes.Buffer
	Create(&buf, &create.Result{Disposition: create.DispositionCreated, SetupInvalidateError: "lock busy"})
	if !strings.Contains(buf.String(), "警告: サーバーの setup 記録を無効化できませんでした: lock busy") {
		t.Fatalf("out = %q", buf.String())
	}
}

// stageLabel は StageID の発行する全識別子を網羅し、未知の値ではパニックする
// （閉じた語彙の扱いを default: panic で統一する方針の固定）。
func TestStageLabelCoversAllStagesAndPanicsOnUnknown(t *testing.T) {
	for _, stage := range []string{
		"", "destination", "branch_check", "fetch", "resolve", "prune", "add", "copy", "canceled",
	} {
		if got := stageLabel(stage); got == "" {
			t.Errorf("stageLabel(%q) is empty", stage)
		}
	}
	defer func() {
		if recover() == nil {
			t.Fatal("unknown stage should panic")
		}
	}()
	stageLabel("bogus")
}
