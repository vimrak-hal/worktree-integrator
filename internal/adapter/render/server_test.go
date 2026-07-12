package render

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/vimrak-hal/worktree-integrator/internal/app/server"
	coreserver "github.com/vimrak-hal/worktree-integrator/internal/core/server"
)

// serverEventLine は 6 種の EventKind それぞれを repo/server タグ付きの行に描画する
// （ライブ表示 Progress.ServerEvent の文言契約）。
func TestServerEventLineRendersEachKind(t *testing.T) {
	var buf bytes.Buffer
	p := NewProgress(&buf)
	for _, ev := range []coreserver.Event{
		{Kind: coreserver.EventAlreadyRunning, Pid: 11},
		{Kind: coreserver.EventStoppingOld, Pid: 22},
		{Kind: coreserver.EventStarted, Pid: 33},
		{Kind: coreserver.EventStopped, Pid: 44},
		{Kind: coreserver.EventStopFailed, Pid: 55, Err: errors.New("kill failed")},
		{Kind: coreserver.EventAlreadyStopped},
	} {
		p.ServerEvent("app", "backend", ev)
	}
	// イベントは入力順にそのまま行へ描画される契約なので、全体を順序込みで照合する。
	want := "  [app/backend] 既に起動中 (pid 11)\n" +
		"  [app/backend] 旧サーバー停止 (pid 22)\n" +
		"  [app/backend] 起動 (pid 33)\n" +
		"  [app/backend] 停止 (pid 44)\n" +
		"  [app/backend] 停止失敗 (pid 55): kill failed（記録は保持されます。再実行で再試行できます）\n" +
		"  [app/backend] 既に停止済み (記録を消去)\n"
	if got := buf.String(); got != want {
		t.Errorf("出力が一致しない:\ngot  %q\nwant %q", got, want)
	}
}

// failureLines は Failure の分類を分岐させる。step は Error の有無で文言が変わり、
// start / stop / other はそれぞれ専用の行になる。
func TestSwitchFailureLines(t *testing.T) {
	outcome := func(f *server.Failure) *server.SwitchResult {
		return &server.SwitchResult{
			PerServer: []server.ServerOutcome{{Repo: "app", Server: "web", Status: server.OutcomeFailed, Failure: f}},
			Failed:    1,
		}
	}
	t.Run("StepWithError", func(t *testing.T) {
		var buf bytes.Buffer
		Switch(&buf, outcome(&server.Failure{Kind: server.FailStep, Step: "on_activate", Error: "boom"}))
		if !strings.Contains(buf.String(), "  [app/web] on_activate 実行エラー: boom\n") {
			t.Fatalf("got %q", buf.String())
		}
	})
	t.Run("StepWithoutError", func(t *testing.T) {
		var buf bytes.Buffer
		Switch(&buf, outcome(&server.Failure{Kind: server.FailStep, Step: "on_activate"}))
		if !strings.Contains(buf.String(), "  [app/web] on_activate 失敗\n") {
			t.Fatalf("got %q", buf.String())
		}
	})
	t.Run("StartWithLogTail", func(t *testing.T) {
		var buf bytes.Buffer
		Switch(&buf, outcome(&server.Failure{Kind: server.FailStart, Error: "spawn", LogTail: []string{"line1", "line2"}}))
		out := buf.String()
		want := "  [app/web] 起動失敗: spawn\n" +
			"  [app/web] --- ログ末尾 ---\n" +
			"  [app/web] | line1\n" +
			"  [app/web] | line2\n"
		if !strings.Contains(out, want) {
			t.Fatalf("got %q, want %q", out, want)
		}
	})
	t.Run("StopFailed", func(t *testing.T) {
		var buf bytes.Buffer
		Switch(&buf, outcome(&server.Failure{Kind: server.FailStop, Error: "kill failed"}))
		if !strings.Contains(buf.String(), "  [app/web] 旧サーバーの停止に失敗したため切り替えを中止: kill failed\n") {
			t.Fatalf("got %q", buf.String())
		}
	})
	t.Run("Other", func(t *testing.T) {
		var buf bytes.Buffer
		Switch(&buf, outcome(&server.Failure{Kind: server.FailOther, Error: "generic"}))
		if !strings.Contains(buf.String(), "  [app/web] エラー: generic\n") {
			t.Fatalf("got %q", buf.String())
		}
	})
}

// Switch はスキップの理由とサマリを描画する。
func TestSwitchSkipsAndSummary(t *testing.T) {
	res := &server.SwitchResult{
		PerServer: []server.ServerOutcome{
			{Repo: "app", Server: "backend", Status: server.OutcomeStarted},
			{Repo: "app", Server: "web", Status: server.OutcomeAlreadyRunning},
			{Repo: "legacy", Server: "old", Status: server.OutcomeSkipped, Reason: server.ReasonNoServerConfig},
			{Repo: "gone", Server: "dev", Status: server.OutcomeSkipped, Reason: server.ReasonMissingWorktree, Path: "/wt/feat/gone"},
		},
		Started: 1, Already: 1, Skipped: 2,
	}
	var buf bytes.Buffer
	Switch(&buf, res)
	out := buf.String()
	for _, want := range []string{
		"  [legacy/old] サーバー設定が無いためスキップ（稼働記録が残っています。server stop で停止できます）\n",
		"  [gone/dev] worktree が無いためスキップ (/wt/feat/gone)\n",
		"\nサマリ: 1 起動, 1 既起動, 2 スキップ, 0 失敗\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
}

// 対象が空のとき、設定の有無で案内が変わる。
func TestSwitchNoTargets(t *testing.T) {
	var buf bytes.Buffer
	Switch(&buf, &server.SwitchResult{NoServerConfig: true})
	if !strings.Contains(buf.String(), "サーバー設定がありません（[servers.*] を設定してください）") {
		t.Fatalf("got %q", buf.String())
	}
	buf.Reset()
	Switch(&buf, &server.SwitchResult{})
	if !strings.Contains(buf.String(), "対象のサーバーがありません") {
		t.Fatalf("got %q", buf.String())
	}
}

// 未知の --repo 名と旧形式ファイルの退避は警告として描画される。
func TestWarnings(t *testing.T) {
	var buf bytes.Buffer
	Status(&buf, &server.StatusResult{
		UnknownRepos: []string{"nope"},
		LegacyBackup: "/state/servers.toml.bak",
	})
	out := buf.String()
	if !strings.Contains(out, "  [nope] サーバー設定がありません（[servers.nope]）\n") {
		t.Fatalf("unknown repo warning missing: %q", out)
	}
	if !strings.Contains(out, "旧形式の状態ファイルを /state/servers.toml.bak へ退避しました") {
		t.Fatalf("legacy warning missing: %q", out)
	}
}

// stateLabel は実行時状態を 状態 列のラベルへ整形する。
func TestStateLabel(t *testing.T) {
	cases := map[string]string{
		server.StateRunning: "稼働中 ✓",
		server.StateCrashed: "クラッシュ ✗",
		server.StateStopped: "停止 -",
	}
	for state, want := range cases {
		if got := stateLabel(state); got != want {
			t.Errorf("stateLabel(%q) = %q, want %q", state, got, want)
		}
	}
}

// Stop はサマリ（0 件 / 停止のみ / 失敗あり）を描画する。
func TestStopSummary(t *testing.T) {
	t.Run("Zero", func(t *testing.T) {
		var buf bytes.Buffer
		Stop(&buf, &server.StopResult{})
		if got := buf.String(); got != "停止対象のサーバーはありません\n" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("StoppedOnly", func(t *testing.T) {
		var buf bytes.Buffer
		Stop(&buf, &server.StopResult{Stopped: 3})
		if got := buf.String(); got != "\nサマリ: 3 停止\n" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("WithFailures", func(t *testing.T) {
		var buf bytes.Buffer
		Stop(&buf, &server.StopResult{Stopped: 1, Failed: 2})
		if got := buf.String(); got != "\nサマリ: 1 停止, 2 失敗\n" {
			t.Fatalf("got %q", got)
		}
	})
}

// SwitchSummary は switch 完了サマリの本文（件数の内訳）を「サマリ:」や改行なしで
// 返す。CLI/MCP（Switch）と TUI（switchCmd）が共有する本文の契約。
func TestSwitchSummaryBody(t *testing.T) {
	got := SwitchSummary(&server.SwitchResult{Started: 1, Already: 1, Skipped: 2, Failed: 0})
	if want := "1 起動, 1 既起動, 2 スキップ, 0 失敗"; got != want {
		t.Fatalf("SwitchSummary = %q, want %q", got, want)
	}
}

// StopSummary は失敗の有無で本文が変わる（失敗 0 件なら停止件数のみ）。
func TestStopSummaryBody(t *testing.T) {
	t.Run("StoppedOnly", func(t *testing.T) {
		if got := StopSummary(&server.StopResult{Stopped: 3}); got != "3 停止" {
			t.Fatalf("StopSummary = %q, want %q", got, "3 停止")
		}
	})
	t.Run("WithFailures", func(t *testing.T) {
		if got := StopSummary(&server.StopResult{Stopped: 1, Failed: 2}); got != "1 停止, 2 失敗" {
			t.Fatalf("StopSummary = %q, want %q", got, "1 停止, 2 失敗")
		}
	})
}

func TestStatusTableTruncatesAliasAndKeepsHeader(t *testing.T) {
	var buf bytes.Buffer
	Status(&buf, &server.StatusResult{Rows: []server.Row{
		{Repo: "app", Server: "backend", Worktree: "feat-a", Alias: strings.Repeat("x", 40), Pid: 1234, State: server.StateRunning},
	}})
	out := buf.String()
	lines := strings.Split(out, "\n")

	// ヘッダー行は REPO 16 / SERVER 12 / WORKTREE 16 / ALIAS 26 / PID 9 の固定幅で
	// 左詰めされ、最後の 状態 列だけ幅を持たない。
	wantHeader := "REPO" + strings.Repeat(" ", 12) +
		"SERVER" + strings.Repeat(" ", 6) +
		"WORKTREE" + strings.Repeat(" ", 8) +
		"ALIAS" + strings.Repeat(" ", 21) +
		"PID" + strings.Repeat(" ", 6) +
		"状態"
	if len(lines) == 0 || lines[0] != wantHeader {
		t.Fatalf("header = %q, want %q", lines[0], wantHeader)
	}
	if !strings.Contains(out, "…") {
		t.Fatalf("long alias should be truncated: %q", out)
	}
	if !strings.Contains(out, "稼働中 ✓") {
		t.Fatalf("missing state: %q", out)
	}
	if !strings.Contains(out, "feat-a") {
		t.Fatalf("missing worktree: %q", out)
	}
}

// 空フィールドは "-" のプレースホルダで描画される。
func TestStatusTablePlaceholders(t *testing.T) {
	var buf bytes.Buffer
	Status(&buf, &server.StatusResult{Rows: []server.Row{
		{Repo: "app", Server: "backend", State: server.StateStopped},
	}})
	out := buf.String()
	if !strings.Contains(out, "-") || !strings.Contains(out, "停止 -") {
		t.Fatalf("placeholders missing: %q", out)
	}
}

// Logs は各エントリのヘッダー・行・欠落・読み取りエラーを描画する。
func TestLogsRendering(t *testing.T) {
	var buf bytes.Buffer
	Logs(&buf, &server.LogsResult{Logs: []server.LogEntry{
		{Repo: "app", Server: "backend", Path: "/logs/a.log", Lines: []string{"l1", "l2"}},
		{Repo: "app", Server: "web", Path: "/logs/w.log", Missing: true},
		{Repo: "app", Server: "db", Path: "/logs/d.log", Error: "permission denied"},
	}})
	out := buf.String()
	for _, want := range []string{
		"==> [app/backend] /logs/a.log <==\nl1\nl2\n",
		"  [app/web] ログがありません (/logs/w.log)\n",
		"==> [app/db] /logs/d.log <==\n  (ログ読み取りエラー: permission denied)\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
}

// 表示できるログが 1 件も無いときは案内を出す。
func TestLogsEmpty(t *testing.T) {
	var buf bytes.Buffer
	Logs(&buf, &server.LogsResult{})
	if !strings.Contains(buf.String(), "表示できるログがありません") {
		t.Fatalf("got %q", buf.String())
	}
}

// FollowHeader は各ログのパス行（欠落は「ログがありません」）を書き出し、追跡できる
// パスの一覧を返す。追跡対象が無ければ案内を書いて空を返す（server logs -f の前段整形）。
func TestFollowHeader(t *testing.T) {
	t.Run("パスと欠落を書き分けて追跡対象を返す", func(t *testing.T) {
		var buf bytes.Buffer
		paths := FollowHeader(&buf, &server.LogsResult{Logs: []server.LogEntry{
			{Repo: "app", Server: "backend", Path: "/logs/a.log"},
			{Repo: "app", Server: "web", Path: "/logs/w.log", Missing: true},
		}})
		if len(paths) != 1 || paths[0] != "/logs/a.log" {
			t.Fatalf("paths = %v", paths)
		}
		out := buf.String()
		if !strings.Contains(out, "  [app/backend] /logs/a.log\n") {
			t.Fatalf("パス行が無い: %q", out)
		}
		if !strings.Contains(out, "  [app/web] ログがありません (/logs/w.log)\n") {
			t.Fatalf("欠落行が無い: %q", out)
		}
		if strings.Contains(out, "表示できるログがありません") {
			t.Fatalf("追跡対象があるのに案内を出している: %q", out)
		}
	})
	t.Run("追跡対象が無ければ案内を書いて空", func(t *testing.T) {
		var buf bytes.Buffer
		paths := FollowHeader(&buf, &server.LogsResult{Logs: []server.LogEntry{
			{Repo: "app", Server: "web", Path: "/logs/w.log", Missing: true},
		}})
		if len(paths) != 0 {
			t.Fatalf("paths = %v", paths)
		}
		if !strings.Contains(buf.String(), "表示できるログがありません") {
			t.Fatalf("案内が無い: %q", buf.String())
		}
	})
}
