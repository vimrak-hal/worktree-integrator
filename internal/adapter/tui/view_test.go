package tui

import (
	"strings"
	"testing"

	"github.com/vimrak-hal/worktree-integrator/internal/app/server"
)

// ステータスビューは resolve が運んだ行を（選択反転・状態ラベル込みで）描画する。
func TestStatusViewShowsRows(t *testing.T) {
	m := newTestModel(t)
	msg := resolved()
	msg.status = &server.StatusResult{Rows: []server.Row{{
		Repo: "app", Server: "backend", Worktree: "feat-x", Pid: 31396, State: server.StateRunning,
	}}}
	m.applyResolved(msg)
	m.Update(key("2"))
	view := m.View()
	for _, want := range []string{"backend", "feat-x", "31396", "稼働中"} {
		if !strings.Contains(view, want) {
			t.Errorf("status view missing %q", want)
		}
	}
}

func TestFitHeightPadsAndTrims(t *testing.T) {
	got := fitHeight([]string{"a\nb", "c"}, 5)
	if len(got) != 5 || got[0] != "a" || got[1] != "b" || got[2] != "c" || got[4] != "" {
		t.Fatalf("fitHeight = %v", got)
	}
	got = fitHeight([]string{"a", "b", "c"}, 2)
	if len(got) != 2 || got[1] != "b" {
		t.Fatalf("fitHeight trim = %v", got)
	}
}
