package server

import (
	"path/filepath"
	"testing"

	"github.com/vimrak-hal/worktree-integrator/internal/infra/statedir"
)

// ParseLogName は LogPath の単射エンコードの逆写像である: エンコード（LogPath）→
// 復号（ParseLogName）の往復で (repo, server, worktree) が完全に戻る。
func TestParseLogNameRoundTripsLogPath(t *testing.T) {
	store := NewStateStore(statedir.At(t.TempDir()))
	cases := []struct{ repo, server, worktree string }{
		{"app", "backend", "feat-x"},
		{"app", "backend", "feature/login"},  // '/' を含む worktree 名
		{"my_app", "back_end", "feat_login"}, // '_' を含む名前（"__" 区切りと衝突しない）
		{"app%25", "srv", "100%"},            // '%' を含む名前
	}
	for _, c := range cases {
		path := store.LogPath(c.repo, c.server, c.worktree)
		repo, server, worktree, prev, ok := ParseLogName(filepath.Base(path))
		if !ok || prev {
			t.Fatalf("ParseLogName(%q) = ok=%v prev=%v", filepath.Base(path), ok, prev)
		}
		if repo != c.repo || server != c.server || worktree != c.worktree {
			t.Fatalf("round trip = (%q, %q, %q), want (%q, %q, %q)",
				repo, server, worktree, c.repo, c.server, c.worktree)
		}

		// .prev（1 世代前のログ）も同じ組へ復号され、prev フラグが立つ。
		_, _, worktree, prev, ok = ParseLogName(filepath.Base(PrevLogPath(path)))
		if !ok || !prev || worktree != c.worktree {
			t.Fatalf("prev round trip failed for %q", filepath.Base(PrevLogPath(path)))
		}
	}
}

// 命名規則に従わないファイル名は ok=false（remove / doctor が他人のファイルに
// 触れないための契約）。
func TestParseLogNameRejectsForeignNames(t *testing.T) {
	for _, base := range []string{
		"README.md",            // .log でない
		"a__b.log",             // コンポーネントが 2 つ
		"a__b__c__d.log",       // コンポーネントが 4 つ
		"a_b__c__d.log",        // 生の '_'（エンコード済みコンポーネントには現れない）
		"a__b__c%2G.log",       // 不正なエスケープ
		"a__b__c%2.log",        // 途中で切れたエスケープ
		"a__b__c.log.previous", // .prev でも .log でもない末尾
	} {
		if _, _, _, _, ok := ParseLogName(base); ok {
			t.Errorf("ParseLogName(%q) = ok, want reject", base)
		}
	}
}
