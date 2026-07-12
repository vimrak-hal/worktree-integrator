package render

import (
	"fmt"
	"io"
	"strings"

	"github.com/vimrak-hal/worktree-integrator/internal/app/tree"
	"github.com/vimrak-hal/worktree-integrator/internal/core/hooks"
)

// List は `list` のテーブル（WORKTREE / ALIAS / REPOS / SERVERS）を描画する。
// 壊れたチェックアウトを含む worktree は "(!)" マークが付き、末尾に doctor の
// 案内を添える。
func List(w io.Writer, res *tree.ListResult) {
	legacyBackupLine(w, res.LegacyBackup)
	if len(res.Worktrees) == 0 {
		fmt.Fprintf(w, "worktree はありません（%s）\n", res.WorktreesDir)
		return
	}
	fmt.Fprintln(w, listRow("WORKTREE", "ALIAS", "REPOS", "SERVERS"))
	anyBroken := false
	for _, row := range res.Worktrees {
		name := row.Name
		if row.Broken {
			name = "(!) " + name
			anyBroken = true
		}
		fmt.Fprintln(w, listRow(
			name,
			truncate(dash(row.Alias), aliasColumnLimit),
			dash(reposCell(row.Repos)),
			dash(serversCell(row.Servers)),
		))
	}
	if anyBroken {
		fmt.Fprintln(w, "\n(!) は壊れたチェックアウトを含む worktree です。doctor --fix で残骸を掃除するか、remove で削除してください。")
	}
}

// listRow は 1 行（または列ヘッダー）を固定の列幅で整形する。
func listRow(name, alias, repos, servers string) string {
	return PadRight(name, 20) + PadRight(alias, 26) + PadRight(repos, 28) + servers
}

// reposCell は REPOS 列のセルを整形する。壊れたチェックアウトには注記が付く。
func reposCell(repos []tree.RepoCell) string {
	parts := make([]string, 0, len(repos))
	for _, r := range repos {
		if r.Healthy {
			parts = append(parts, r.Repo)
		} else {
			parts = append(parts, r.Repo+"（壊れた gitdir）")
		}
	}
	return strings.Join(parts, ", ")
}

// serversCell は SERVERS 列のセル（稼働中のサーバー一覧）を整形する。
func serversCell(servers []tree.ServerCell) string {
	parts := make([]string, 0, len(servers))
	for _, s := range servers {
		parts = append(parts, fmt.Sprintf("%s/%s: 稼働中 (pid %d)", s.Repo, s.Server, s.Pid))
	}
	return strings.Join(parts, ", ")
}

// Enter は `enter` の結果（after フックの実行結果と遷移先）を描画する。フックが
// 失敗している場合は完了行を出さない（エラーは main が stderr に書く）。
func Enter(w io.Writer, res *tree.EnterResult) {
	failed := false
	for _, h := range res.Hooks {
		fmt.Fprint(w, hookLine(h))
		failed = failed || h.Status == hooks.ReportFailed
	}
	if !failed {
		fmt.Fprintf(w, "worktree %q に入りました（%s）\n", res.Worktree, res.Root)
	}
}

// Remove は `remove` の結果を描画する。サーバー停止のライブイベント行（Progress）は
// 描画済みという前提で、各ステップの結末を書き出す。
func Remove(w io.Writer, res *tree.RemoveResult) {
	legacyBackupLine(w, res.LegacyBackup)
	if res.Stop != nil && res.Stop.Failed > 0 {
		fmt.Fprintf(w, "サーバー停止に失敗したため削除を中断しました（%d 件）\n", res.Stop.Failed)
		return
	}
	if res.Stop != nil && res.Stop.Stopped > 0 {
		fmt.Fprintf(w, "稼働中のサーバーを停止しました: %d 件\n", res.Stop.Stopped)
	}

	for _, r := range res.Repos {
		switch {
		case r.Error != "" && !r.Removed:
			fmt.Fprintf(w, "  ✗ %s: %s\n", r.Repo, r.Error)
		case r.Error != "":
			// チェックアウトは消えたが、ブランチの後始末に失敗した。
			fmt.Fprintf(w, "  ! %s: worktree を削除しました（ブランチの削除に失敗: %s）\n", r.Repo, r.Error)
		case r.BranchDeleted:
			fmt.Fprintf(w, "  ✓ %s: worktree とブランチ %q を削除しました\n", r.Repo, res.Worktree)
		default:
			fmt.Fprintf(w, "  ✓ %s: worktree を削除しました\n", r.Repo)
		}
	}

	if res.SetupCleared > 0 {
		fmt.Fprintf(w, "setup 記録を %d 件削除しました\n", res.SetupCleared)
	}
	if res.AliasRemoved {
		fmt.Fprintln(w, "別名を削除しました")
	}
	if n := len(res.LogsRemoved); n > 0 {
		fmt.Fprintf(w, "ログを %d 件削除しました\n", n)
	}
	for _, e := range []struct{ label, err string }{
		{"setup 記録の削除", res.StateError},
		{"別名の削除", res.AliasError},
		{"ログの削除", res.LogsError},
		{"ルートディレクトリの削除", res.RootError},
	} {
		if e.err != "" {
			fmt.Fprintf(w, "警告: %sに失敗しました: %s\n", e.label, e.err)
		}
	}
	if res.RootRemoved {
		fmt.Fprintf(w, "worktree %q を削除しました（%s）\n", res.Worktree, res.Root)
	} else if res.RootError == "" {
		fmt.Fprintf(w, "worktree %q のルートは削除されていません（%s）\n", res.Worktree, res.Root)
	}
}

// Doctor は `doctor` の発見一覧と修復サマリを描画する。
func Doctor(w io.Writer, res *tree.DoctorResult) {
	legacyBackupLine(w, res.LegacyBackup)
	if len(res.Findings) == 0 {
		fmt.Fprintln(w, "問題は見つかりませんでした")
		return
	}
	for _, f := range res.Findings {
		fmt.Fprintf(w, "  %s %s%s\n", findingMark(f), findingLine(f), fixNote(f))
		// git の出力など複数行の詳細はインデントして添える。
		if f.Check == "prunable_worktrees" && f.Detail != "" {
			for line := range strings.SplitSeq(strings.TrimRight(f.Detail, "\n"), "\n") {
				fmt.Fprintf(w, "      | %s\n", line)
			}
		}
	}
	switch {
	case res.Fix:
		fmt.Fprintf(w, "\nサマリ: %d 件の発見, %d 件を修復, %d 件の修復に失敗\n",
			len(res.Findings), res.Fixed, res.FixFailed)
	case anyFixable(res.Findings):
		fmt.Fprintf(w, "\nサマリ: %d 件の発見（--fix で修復できます）\n", len(res.Findings))
	default:
		fmt.Fprintf(w, "\nサマリ: %d 件の発見（報告のみ）\n", len(res.Findings))
	}
}

// findingMark は発見の状態を表す記号。修復済み ✓ / 修復失敗 ✗ / 未修復（報告）-。
func findingMark(f tree.Finding) string {
	switch {
	case f.Fixed:
		return "✓"
	case f.FixError != "":
		return "✗"
	default:
		return "-"
	}
}

// findingLine は 1 つの発見をユーザー向けの日本語 1 行に変換する。Check は doctor が
// 発行する閉じた語彙のため、未知の値はバグでありパニックさせる。
func findingLine(f tree.Finding) string {
	switch f.Check {
	case "dead_running":
		return fmt.Sprintf("[%s/%s] 稼働記録のプロセス（%s）は消滅しています（worktree %s）",
			f.Repo, f.Server, f.Detail, f.Worktree)
	case "stale_setup":
		return fmt.Sprintf("[%s/%s] 存在しない worktree %q の setup 記録が残っています",
			f.Repo, f.Server, f.Worktree)
	case "stale_alias":
		return fmt.Sprintf("存在しない worktree %q に別名 %q が残っています", f.Worktree, f.Detail)
	case "orphan_log":
		return fmt.Sprintf("どこからも参照されないログが残っています（%s）", f.Path)
	case "prunable_worktrees":
		if !f.Fixable {
			return fmt.Sprintf("[%s] worktree メタデータを検査できません: %s", f.Repo, f.Detail)
		}
		return fmt.Sprintf("[%s] 掃除できる worktree メタデータがあります", f.Repo)
	case "broken_repo":
		return fmt.Sprintf("[%s] .git が有効なリポジトリを指していません（%s）", f.Repo, f.Path)
	case "config_without_repo":
		return fmt.Sprintf("[%s] 設定 [repos.%s] がありますが、repos_dir に実在しません", f.Repo, f.Repo)
	case "repo_without_servers":
		return fmt.Sprintf("[%s] サーバー設定（[repos.%s.servers]）がありません", f.Repo, f.Repo)
	default:
		panic(fmt.Sprintf("unknown doctor check %q", f.Check))
	}
}

// fixNote は修復の結末を行末に添える。
func fixNote(f tree.Finding) string {
	switch {
	case f.Fixed:
		return " → 修復しました"
	case f.FixError != "":
		return " → 修復に失敗: " + f.FixError
	default:
		return ""
	}
}

// anyFixable は修復可能な発見が 1 件でもあるかを返す。
func anyFixable(findings []tree.Finding) bool {
	for _, f := range findings {
		if f.Fixable {
			return true
		}
	}
	return false
}
