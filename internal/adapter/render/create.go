package render

import (
	"fmt"
	"io"

	"github.com/vimrak-hal/worktree-integrator/internal/app/create"
)

// Create は create ワークフローの結果を描画する。ライブの途中経過（Progress）は
// 描画済みという前提で、フックの結果・リポジトリごとのサマリ・警告のみを書き出す。
func Create(w io.Writer, res *create.Result) {
	switch res.Disposition {
	case create.DispositionNothingToAdd:
		hookGroup(w, res, "before")
		fmt.Fprintf(w, "追加するリポジトリはありません（worktree %q は全リポジトリで作成済みです）\n", res.Worktree)
		hookGroup(w, res, "after")
		return
	case create.DispositionAborted:
		hookGroup(w, res, "before")
		return
	case create.DispositionNoRepos:
		hookGroup(w, res, "before")
		fmt.Fprintf(w, "リポジトリが見つかりません（%s）\n", res.ReposDir)
		return
	case create.DispositionNothingSelected:
		hookGroup(w, res, "before")
		fmt.Fprintln(w, "リポジトリが選択されませんでした。何もしません。")
		return
	case create.DispositionCreated:
		// 続行して全体を描画する。
	default:
		panic(fmt.Sprintf("unknown create.Disposition %q", res.Disposition))
	}

	hookGroup(w, res, "before")
	hookGroup(w, res, "after_worktree")
	hookGroup(w, res, "after")

	fmt.Fprintf(w, "\nサマリ: %d 作成, %d スキップ, %d 失敗\n", res.Created, res.Skipped, res.Failed)
	for _, r := range res.Repos {
		if r.Status == create.RepoCreated {
			if r.Copy != nil && len(r.Copy.Failures) > 0 {
				fmt.Fprintf(w, "  ✓ %s（コピー失敗 %d 件）\n", r.Repo, len(r.Copy.Failures))
			} else {
				fmt.Fprintf(w, "  ✓ %s\n", r.Repo)
			}
		}
	}
	for _, r := range res.Repos {
		if r.Status == create.RepoSkipped {
			fmt.Fprintf(w, "  - %s (スキップ: %s)\n", r.Repo, skipLabel(r.Stage))
		}
	}
	for _, r := range res.Repos {
		if r.Status == create.RepoFailed {
			errMsg := r.Error
			if errMsg == "" {
				errMsg = "不明なエラー"
			}
			fmt.Fprintf(w, "  ✗ %s (%s: %s)\n", r.Repo, stageLabel(r.Stage), errMsg)
		}
	}

	if res.SetupInvalidateError != "" {
		fmt.Fprintf(w, "警告: サーバーの setup 記録を無効化できませんでした: %s\n", res.SetupInvalidateError)
	}
}

// hookGroup は 1 つのタイミングのフック結果を、件数付きのヘッダーに続けて 1 行ずつ
// 書き出す。そのタイミングの結果が無ければ何も書き出さない。
func hookGroup(w io.Writer, res *create.Result, timing string) {
	var group []create.HookOutcome
	for _, h := range res.Hooks {
		if h.Timing == timing {
			group = append(group, h)
		}
	}
	if len(group) == 0 {
		return
	}
	if timing == "after_worktree" {
		fmt.Fprintf(w, "after_worktree フックを %d リポジトリで実行:\n", res.Created)
	} else {
		fmt.Fprintf(w, "%s フックを %d 件実行:\n", timing, len(group))
	}
	for _, h := range group {
		fmt.Fprint(w, hookLine(h))
	}
}

// hookLine は 1 つのフック結果を記号付きの 1 行に整形する。Status は閉じた語彙の
// ため、未知の値はバグでありパニックさせる。
func hookLine(h create.HookOutcome) string {
	switch h.Status {
	case create.HookSucceeded:
		return fmt.Sprintf("  ✓ hook %s 完了\n", h.Name)
	case create.HookWarned:
		return fmt.Sprintf("  ! hook %s 失敗（許容）: %s\n", h.Name, h.Detail)
	case create.HookFailed:
		return fmt.Sprintf("  ✗ hook %s 失敗: %s\n", h.Name, h.Detail)
	default:
		panic(fmt.Sprintf("unknown hook status %q", h.Status))
	}
}

// stageLabel は処理段階の識別子をユーザー向けの（日本語の）ラベルに変換する。
// 識別子は create.StageID が発行する閉じた語彙のため、未知の値はバグであり
// パニックさせる。
func stageLabel(stage string) string {
	switch stage {
	case "":
		return "-"
	case "destination":
		return "作成先の確認"
	case "branch_check":
		return "ブランチ確認"
	case "fetch":
		return "fetch"
	case "resolve":
		return "main ブランチ解決"
	case "prune":
		return "worktree prune"
	case "add":
		return "worktree 作成"
	case "copy":
		return "ファイルコピー"
	case "canceled":
		return "キャンセル"
	default:
		panic(fmt.Sprintf("unknown stage id %q", stage))
	}
}

// skipLabel はスキップの理由をユーザー向けのラベルに変換する。スキップは
// 「既存の worktree（作成先の検査で検出）」か「キャンセルによる未着手」のいずれか。
func skipLabel(stage string) string {
	switch stage {
	case "destination":
		return "worktree は既に存在します"
	case "canceled":
		return "キャンセル"
	default:
		return stageLabel(stage)
	}
}
