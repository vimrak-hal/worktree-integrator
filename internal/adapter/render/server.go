package render

import (
	"fmt"
	"io"
	"strconv"

	"github.com/vimrak-hal/worktree-integrator/internal/app/server"
)

// aliasColumnLimit はステータステーブルに表示する別名の最大文字数。これより長い
// 別名は切り詰められ、後続の列の整列が崩れないようにする。
const aliasColumnLimit = 25

// warnings は各 Result 共通の警告（旧形式ファイルの退避・未知のリポジトリ名）を
// 書き出す。
func warnings(w io.Writer, legacyBackup string, unknownRepos []string) {
	if legacyBackup != "" {
		fmt.Fprintf(w, "旧形式の状態ファイルを %s へ退避しました。以前から稼働中のサーバーは追跡されていません。手動で停止してください。\n", legacyBackup)
	}
	for _, repo := range unknownRepos {
		tagged(w, repo, "サーバー設定がありません（[servers.%s]）", repo)
	}
}

// noTargets は対象が 1 件も無いときの案内を書き出す。
func noTargets(w io.Writer, noServerConfig bool) {
	if noServerConfig {
		fmt.Fprintln(w, "サーバー設定がありません（[servers.*] を設定してください）")
	} else {
		fmt.Fprintln(w, "対象のサーバーがありません")
	}
}

// Switch は `server switch` の結果を描画する。ライブのイベント行（Progress）は
// 描画済みという前提で、スキップ・失敗の詳細とサマリのみを書き出す。
func Switch(w io.Writer, res *server.SwitchResult) {
	warnings(w, res.LegacyBackup, res.UnknownRepos)
	if len(res.PerServer) == 0 {
		noTargets(w, res.NoServerConfig)
		return
	}
	for _, o := range res.PerServer {
		tag := o.Repo + "/" + o.Server
		switch o.Status {
		case server.OutcomeSkipped:
			switch o.Reason {
			case server.ReasonNoServerConfig:
				tagged(w, tag, "サーバー設定が無いためスキップ（稼働記録が残っています。server stop で停止できます）")
			case server.ReasonMissingWorktree:
				tagged(w, tag, "worktree が無いためスキップ (%s)", o.Path)
			default:
				panic(fmt.Sprintf("unknown skip reason %q", o.Reason))
			}
		case server.OutcomeFailed:
			failureLines(w, tag, o.Failure)
		}
	}
	fmt.Fprintf(w, "\nサマリ: %d 起動, %d 既起動, %d スキップ, %d 失敗\n",
		res.Started, res.Already, res.Skipped, res.Failed)
}

// failureLines は切り替えに失敗した際のユーザー向けの行を書き出す。Failure.Kind は
// 閉じた語彙のため、未知の値はバグでありパニックさせる。
func failureLines(w io.Writer, tag string, f *server.Failure) {
	if f == nil {
		tagged(w, tag, "エラー: 詳細不明")
		return
	}
	switch f.Kind {
	case server.FailStep:
		if f.Error != "" {
			tagged(w, tag, "%s 実行エラー: %s", f.Step, f.Error)
		} else {
			tagged(w, tag, "%s 失敗", f.Step)
		}
	case server.FailStart:
		tagged(w, tag, "起動失敗: %s", f.Error)
		// 即死検出はログ末尾を添えて返す。原因の手掛かりをその場で提示する。
		if len(f.LogTail) > 0 {
			tagged(w, tag, "--- ログ末尾 ---")
			for _, line := range f.LogTail {
				tagged(w, tag, "| %s", line)
			}
		}
	case server.FailStop:
		tagged(w, tag, "旧サーバーの停止に失敗したため切り替えを中止: %s", f.Error)
	case server.FailOther:
		tagged(w, tag, "エラー: %s", f.Error)
	default:
		panic(fmt.Sprintf("unknown failure kind %q", f.Kind))
	}
}

// Stop は `server stop` の結果を描画する。ライブのイベント行（Progress）は描画済み
// という前提で、サマリのみを書き出す。
func Stop(w io.Writer, res *server.StopResult) {
	warnings(w, res.LegacyBackup, res.UnknownRepos)
	if res.Stopped == 0 && res.Failed == 0 {
		fmt.Fprintln(w, "停止対象のサーバーはありません")
		return
	}
	if res.Failed > 0 {
		fmt.Fprintf(w, "\nサマリ: %d 停止, %d 失敗\n", res.Stopped, res.Failed)
	} else {
		fmt.Fprintf(w, "\nサマリ: %d 停止\n", res.Stopped)
	}
}

// Status は `server status` のテーブルを描画する。
func Status(w io.Writer, res *server.StatusResult) {
	warnings(w, res.LegacyBackup, res.UnknownRepos)
	if len(res.Rows) == 0 {
		noTargets(w, res.NoServerConfig)
		return
	}
	fmt.Fprintln(w, statusRow("REPO", "SERVER", "WORKTREE", "ALIAS", "PID", "状態"))
	for _, r := range res.Rows {
		pid := "-"
		if r.Pid != 0 {
			pid = strconv.Itoa(r.Pid)
		}
		fmt.Fprintln(w, statusRow(
			r.Repo, r.Server, dash(r.Worktree), truncate(dash(r.Alias), aliasColumnLimit),
			pid, stateLabel(r.State)))
	}
}

// statusRow は 1 行（または列ヘッダーを与えればヘッダー行）を固定の列幅で整形する。
func statusRow(repo, srv, worktree, alias, pid, state string) string {
	return PadRight(repo, 16) + PadRight(srv, 12) + PadRight(worktree, 16) +
		PadRight(alias, 26) + PadRight(pid, 9) + state
}

// stateLabel はサーバーの実行時状態の識別子を、ステータステーブルの 状態 列に表示
// されるラベルに整形する。識別子は閉じた語彙のため、未知の値はバグでありパニック
// させる。
func stateLabel(state string) string {
	switch state {
	case server.StateRunning:
		return "稼働中 ✓"
	case server.StateCrashed:
		return "クラッシュ ✗"
	case server.StateStopped:
		return "停止 -"
	default:
		panic(fmt.Sprintf("unknown server state %q", state))
	}
}

// Logs は `server logs` の結果（各ログの末尾）を描画する。
func Logs(w io.Writer, res *server.LogsResult) {
	warnings(w, res.LegacyBackup, res.UnknownRepos)
	shown := false
	for _, entry := range res.Logs {
		tag := entry.Repo + "/" + entry.Server
		if entry.Missing {
			tagged(w, tag, "ログがありません (%s)", entry.Path)
			continue
		}
		shown = true
		fmt.Fprintf(w, "==> [%s] %s <==\n", tag, entry.Path)
		if entry.Error != "" {
			fmt.Fprintf(w, "  (ログ読み取りエラー: %s)\n", entry.Error)
			continue
		}
		for _, line := range entry.Lines {
			fmt.Fprintln(w, line)
		}
	}
	if !shown && !anyMissing(res.Logs) {
		fmt.Fprintln(w, "表示できるログがありません")
	}
}

// anyMissing は Missing エントリが 1 件でもあるかを返す（「表示できるログが
// ありません」との二重案内を避けるため）。
func anyMissing(entries []server.LogEntry) bool {
	for _, e := range entries {
		if e.Missing {
			return true
		}
	}
	return false
}
