package render

import (
	"bytes"
	"strings"
	"testing"

	"github.com/vimrak-hal/worktree-integrator/internal/app/create"
	"github.com/vimrak-hal/worktree-integrator/internal/app/server"
	"github.com/vimrak-hal/worktree-integrator/internal/app/tree"
)

// list テーブル: 別名・リポジトリ・稼働サーバーが列に並び、壊れた worktree には
// (!) マークと doctor の案内が付く。
func TestListTable(t *testing.T) {
	res := &tree.ListResult{
		WorktreesDir: "/wt",
		Worktrees: []tree.WorktreeRow{
			{
				Name: "ABC-123", Root: "/wt/ABC-123", Alias: "ログイン画面の修正",
				Repos: []tree.RepoCell{
					{Repo: "api", Branch: "ABC-123", Healthy: true},
					{Repo: "web", Branch: "ABC-123", Healthy: true},
				},
				Servers: []tree.ServerCell{{Repo: "api", Server: "backend", Pid: 4242}},
			},
			{
				Name: "old-x", Root: "/wt/old-x", Broken: true,
				Repos: []tree.RepoCell{{Repo: "web", Healthy: false}},
			},
		},
	}
	var buf bytes.Buffer
	List(&buf, res)
	out := buf.String()
	for _, want := range []string{
		"WORKTREE", "ALIAS", "REPOS", "SERVERS",
		"ABC-123", "ログイン画面の修正", "api, web", "api/backend: 稼働中 (pid 4242)",
		"(!) old-x", "web（壊れた gitdir）",
		"doctor --fix",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// worktree が無ければその旨の案内、壊れた worktree が無ければ doctor 案内は出ない。
func TestListEmptyAndNoBrokenHint(t *testing.T) {
	var buf bytes.Buffer
	List(&buf, &tree.ListResult{WorktreesDir: "/wt", Worktrees: []tree.WorktreeRow{}})
	if !strings.Contains(buf.String(), "worktree はありません（/wt）") {
		t.Fatalf("out = %q", buf.String())
	}

	buf.Reset()
	List(&buf, &tree.ListResult{WorktreesDir: "/wt", Worktrees: []tree.WorktreeRow{
		{Name: "ok", Repos: []tree.RepoCell{{Repo: "api", Healthy: true}}},
	}})
	if strings.Contains(buf.String(), "doctor") {
		t.Fatalf("healthy list must not mention doctor: %q", buf.String())
	}
}

// enter: フック行と完了行。フック失敗時は完了行を出さない。
func TestEnterRendering(t *testing.T) {
	var buf bytes.Buffer
	Enter(&buf, &tree.EnterResult{Worktree: "feat", Root: "/wt/feat",
		Hooks: []create.HookOutcome{{Timing: "after", Name: "nav", Status: create.HookSucceeded}}})
	out := buf.String()
	if !strings.Contains(out, "✓ hook nav 完了") ||
		!strings.Contains(out, `worktree "feat" に入りました（/wt/feat）`) {
		t.Fatalf("out = %q", out)
	}

	buf.Reset()
	Enter(&buf, &tree.EnterResult{Worktree: "feat", Root: "/wt/feat",
		Hooks: []create.HookOutcome{{Timing: "after", Name: "nav", Status: create.HookFailed, Detail: "boom"}}})
	if strings.Contains(buf.String(), "入りました") {
		t.Fatalf("failed hook must suppress the completion line: %q", buf.String())
	}
}

// remove: 各ステップの結末が描画される。
func TestRemoveRendering(t *testing.T) {
	res := &tree.RemoveResult{
		Worktree: "feat", Root: "/wt/feat",
		Stop: &server.StopResult{Stopped: 1},
		Repos: []tree.RepoRemoval{
			{Repo: "api", Removed: true, BranchDeleted: true},
			{Repo: "web", Removed: false, Error: "contains modified or untracked files"},
		},
		SetupCleared: 1,
		AliasRemoved: true,
		LogsRemoved:  []string{"/logs/a.log", "/logs/a.log.prev"},
	}
	var buf bytes.Buffer
	Remove(&buf, res)
	out := buf.String()
	for _, want := range []string{
		"稼働中のサーバーを停止しました: 1 件",
		`✓ api: worktree とブランチ "feat" を削除しました`,
		"✗ web: contains modified or untracked files",
		"setup 記録を 1 件削除しました",
		"別名を削除しました",
		"ログを 2 件削除しました",
		`worktree "feat" のルートは削除されていません（/wt/feat）`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	// 完全に成功した削除は完了行になる。
	buf.Reset()
	Remove(&buf, &tree.RemoveResult{
		Worktree: "feat", Root: "/wt/feat",
		Repos:       []tree.RepoRemoval{{Repo: "api", Removed: true}},
		RootRemoved: true,
	})
	if !strings.Contains(buf.String(), `worktree "feat" を削除しました（/wt/feat）`) {
		t.Fatalf("out = %q", buf.String())
	}

	// サーバー停止失敗は中断の案内のみ。
	buf.Reset()
	Remove(&buf, &tree.RemoveResult{
		Worktree: "feat", Root: "/wt/feat",
		Stop: &server.StopResult{Failed: 1},
	})
	if !strings.Contains(buf.String(), "サーバー停止に失敗したため削除を中断しました（1 件）") {
		t.Fatalf("out = %q", buf.String())
	}
}

// doctor: 発見ゼロ・報告のみ・--fix 済みのそれぞれのサマリと、全チェック識別子の
// 日本語行。未知の識別子はパニックする。
func TestDoctorRendering(t *testing.T) {
	var buf bytes.Buffer
	Doctor(&buf, &tree.DoctorResult{Findings: []tree.Finding{}})
	if !strings.Contains(buf.String(), "問題は見つかりませんでした") {
		t.Fatalf("out = %q", buf.String())
	}

	findings := []tree.Finding{
		{Check: "dead_running", Repo: "api", Server: "backend", Worktree: "feat", Detail: "pid 42", Fixable: true},
		{Check: "stale_setup", Repo: "api", Server: "backend", Worktree: "gone", Fixable: true},
		{Check: "stale_alias", Worktree: "gone", Detail: "Login", Fixable: true},
		{Check: "orphan_log", Path: "/logs/x.log", Fixable: true},
		{Check: "prunable_worktrees", Repo: "api", Detail: "Removing worktrees/x", Fixable: true},
		{Check: "broken_repo", Repo: "fake", Path: "/repos/fake"},
		{Check: "config_without_repo", Repo: "ghost"},
		{Check: "repo_without_servers", Repo: "plain", Path: "/repos/plain"},
	}
	buf.Reset()
	Doctor(&buf, &tree.DoctorResult{Findings: findings})
	out := buf.String()
	for _, want := range []string{
		"[api/backend] 稼働記録のプロセス（pid 42）は消滅しています",
		`存在しない worktree "gone" の setup 記録`,
		`別名 "Login" が残っています`,
		"どこからも参照されないログ",
		"掃除できる worktree メタデータ",
		"| Removing worktrees/x",
		".git が有効なリポジトリを指していません",
		"[ghost] 設定 [repos.ghost] がありますが、repos_dir に実在しません",
		"[plain] サーバー設定（[repos.plain.servers]）がありません",
		"サマリ: 8 件の発見（--fix で修復できます）",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	// --fix 済み: 修復の結末が行末とサマリに現れる。
	fixed := make([]tree.Finding, len(findings))
	copy(fixed, findings)
	fixed[0].Fixed = true
	fixed[1].FixError = "lock busy"
	buf.Reset()
	Doctor(&buf, &tree.DoctorResult{Findings: fixed, Fix: true, Fixed: 1, FixFailed: 1})
	out = buf.String()
	for _, want := range []string{"→ 修復しました", "→ 修復に失敗: lock busy", "サマリ: 8 件の発見, 1 件を修復, 1 件の修復に失敗"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	defer func() {
		if recover() == nil {
			t.Fatal("unknown check id should panic")
		}
	}()
	findingLine(tree.Finding{Check: "bogus"})
}
