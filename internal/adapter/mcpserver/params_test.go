package mcpserver

import (
	"testing"

	"github.com/vimrak-hal/worktree-integrator/internal/core/action"
)

// TestScopeFromPtr は、パラメータ省略（nil）のみが「全 worktree」(AllWorktrees) に
// なり、明示的な空文字列は不正名エラーになることを固定する（意図的な仕様変更:
// 旧実装は空文字列を全件対象へ正規化していた）。
func TestScopeFromPtr(t *testing.T) {
	scope, err := scopeFromPtr(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := scope.(action.AllWorktrees); !ok {
		t.Fatalf("nil should scope to all worktrees, got %T", scope)
	}

	empty := ""
	if _, err := scopeFromPtr(&empty); err == nil {
		t.Fatal("empty string should be an invalid-name error")
	}

	name := "feat-x"
	scope, err = scopeFromPtr(&name)
	if err != nil {
		t.Fatal(err)
	}
	one, ok := scope.(action.OneWorktree)
	if !ok || one.Name.String() != "feat-x" {
		t.Fatalf("named should scope to OneWorktree{feat-x}, got %#v", scope)
	}
}

// clampLines は 0 以下（省略）を既定 50 に、上限超過を 2000 に収める。
func TestClampLines(t *testing.T) {
	cases := map[int]int{
		0:     50,
		-5:    50,
		1:     1,
		120:   120,
		2000:  2000,
		2001:  2000,
		99999: 2000,
	}
	for in, want := range cases {
		if got := clampLines(in); got != want {
			t.Errorf("clampLines(%d) = %d, want %d", in, got, want)
		}
	}
}
