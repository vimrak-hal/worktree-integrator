package wtenv_test

import (
	"reflect"
	"testing"

	"github.com/vimrak-hal/worktree-integrator/internal/core/wtenv"
)

// pairsToMap は Pair のスライスをキー→値のマップに変換し、重複キーがあれば
// テストを失敗させる。契約は「キー集合とその値」であり、順序は実装詳細である
// （環境変数として渡る時点で順序は意味を失う）。
func pairsToMap(t *testing.T, pairs []wtenv.Pair) map[string]string {
	t.Helper()
	m := make(map[string]string, len(pairs))
	for _, p := range pairs {
		if _, dup := m[p.Key]; dup {
			t.Fatalf("キー %q が重複している", p.Key)
		}
		m[p.Key] = p.Value
	}
	return m
}

// NewRunContext は Root を <worktreesDir>/<worktreeName> として導出する。
func TestNewRunContextDerivesRoot(t *testing.T) {
	run := wtenv.NewRunContext("feature-x", "/repos", "/worktrees")
	want := &wtenv.RunContext{
		WorktreeName: "feature-x",
		ReposDir:     "/repos",
		WorktreesDir: "/worktrees",
		Root:         "/worktrees/feature-x",
	}
	if !reflect.DeepEqual(run, want) {
		t.Fatalf("NewRunContext = %#v, want %#v", run, want)
	}
	// 階層的な名前でも Root は単純なパス結合になる。
	if got := wtenv.NewRunContext("feat/sub", "/r", "/w").Root; got != "/w/feat/sub" {
		t.Fatalf("Root = %q", got)
	}
}

// EnvPairs は repo が nil のとき、run 由来の 4 変数（キー集合と値）を返す。
func TestEnvPairsRunOnly(t *testing.T) {
	run := wtenv.NewRunContext("feature-x", "/repos", "/worktrees")

	got := pairsToMap(t, wtenv.EnvPairs(run, nil))

	want := map[string]string{
		"WT_WORKTREE_NAME": "feature-x",
		"WT_REPOS_DIR":     "/repos",
		"WT_WORKTREES_DIR": "/worktrees",
		"WT_ROOT":          "/worktrees/feature-x",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("EnvPairs(run, nil) = %#v, want %#v", got, want)
	}
}

// EnvPairs は repo が非 nil のとき、run の 4 変数に repo の 3 変数を加えた
// 計 7 変数（キー集合と値）を返す。
func TestEnvPairsWithRepo(t *testing.T) {
	run := wtenv.NewRunContext("feature-x", "/repos", "/worktrees")
	repo := &wtenv.RepoContext{
		RepoName:     "myapp",
		RepoPath:     "/repos/myapp",
		WorktreePath: "/worktrees/feature-x/myapp",
	}

	got := pairsToMap(t, wtenv.EnvPairs(run, repo))

	want := map[string]string{
		"WT_WORKTREE_NAME": "feature-x",
		"WT_REPOS_DIR":     "/repos",
		"WT_WORKTREES_DIR": "/worktrees",
		"WT_ROOT":          "/worktrees/feature-x",
		"WT_REPO_NAME":     "myapp",
		"WT_REPO_PATH":     "/repos/myapp",
		"WT_WORKTREE_PATH": "/worktrees/feature-x/myapp",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("EnvPairs(run, repo) = %#v, want %#v", got, want)
	}
}

// Environ は base を先頭に保ち、その後ろに "KEY=VALUE" を append する。
func TestEnvironAppendsToBase(t *testing.T) {
	base := []string{"PATH=/usr/bin", "HOME=/home/u"}
	pairs := []wtenv.Pair{
		{Key: "WT_ROOT", Value: "/worktrees/feature-x"},
		{Key: "WT_REPO_NAME", Value: "myapp"},
	}

	got := wtenv.Environ(base, pairs)

	want := []string{
		"PATH=/usr/bin",
		"HOME=/home/u",
		"WT_ROOT=/worktrees/feature-x",
		"WT_REPO_NAME=myapp",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Environ(base, pairs) = %#v, want %#v", got, want)
	}
}

// Environ は引数の base スライスを破壊的に変更してはならない
// （内部で新しいスライスへコピーしていることを検証）。
func TestEnvironDoesNotMutateBase(t *testing.T) {
	base := []string{"PATH=/usr/bin"}
	baseCopy := append([]string{}, base...)
	pairs := []wtenv.Pair{{Key: "WT_ROOT", Value: "/root"}}

	_ = wtenv.Environ(base, pairs)

	if !reflect.DeepEqual(base, baseCopy) {
		t.Fatalf("Environ mutated base: got %#v, want %#v", base, baseCopy)
	}
}

// Environ は pairs が空でも base のコピーをそのまま返す。
// さらに返り値は base とは独立した新しいスライスであり、
// 返り値への書き込みが base を破壊しないことも検証する。
func TestEnvironEmptyPairs(t *testing.T) {
	base := []string{"A=1", "B=2"}

	got := wtenv.Environ(base, nil)

	if !reflect.DeepEqual(got, base) {
		t.Fatalf("Environ(base, nil) = %#v, want %#v", got, base)
	}

	// got は base のコピーであり、書き換えても base に波及しない。
	if len(got) > 0 {
		got[0] = "MUTATED=1"
		if base[0] != "A=1" {
			t.Fatalf("Environ(base, nil) は base と backing array を共有している: base[0]=%q", base[0])
		}
	}
}

// 空の Value を持つペアでも "KEY=" の形式で連結される。
func TestEnvironEmptyValue(t *testing.T) {
	got := wtenv.Environ(nil, []wtenv.Pair{{Key: "WT_ROOT", Value: ""}})

	want := []string{"WT_ROOT="}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Environ with empty value = %#v, want %#v", got, want)
	}
}
