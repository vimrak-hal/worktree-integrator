package worktree

import (
	"context"

	"github.com/vimrak-hal/worktree-integrator/internal/infra/parallel"
)

// Run はすべてのリクエストを concurrency 個のワーカーを上限として並列処理し、
// 収集した結果をリクエスト順で返す。1 つのリポジトリが失敗しても他の処理は
// 止まらない。進捗は reporter を通じて書き出され、reporter は並行利用に対して
// 安全でなければならない。
//
// ctx がキャンセルされると新規の着手を停止し、未着手のリポジトリは
// StatusSkipped（Stage=StageCanceled）として集約する。実行中のリポジトリは
// git プロセスが終了させられ、その段階の失敗として Outcome に現れる。
func Run(ctx context.Context, reqs []Request, concurrency int, reporter Reporter) []Outcome {
	return parallel.Map(ctx, concurrency, reqs, func(ctx context.Context, _ int, req Request) Outcome {
		// キャンセル後に着手しなかったリポジトリは、git に触れず
		// StatusSkipped（StageCanceled）として集約する。parallel.Map は
		// 未着手の項目にキャンセル済みの ctx を渡してこの fn を呼ぶ。
		if ctx.Err() != nil {
			return skipCanceled(req)
		}
		return Process(ctx, req, reporter)
	})
}

// skipCanceled は、キャンセルにより着手しなかったリポジトリの Outcome を合成する。
// 終端状態は Reporter へは報告しない（Outcome に一本化されている）。
func skipCanceled(req Request) Outcome {
	return Outcome{Repo: req.RepoName, Status: StatusSkipped, Stage: StageCanceled}
}

// Concurrency は並列処理するリポジトリ数を決定する。リポジトリ数より多くの
// ワーカーを起動しても無意味なため、結果は repoCount を上限とする。requested が
// 正であればそれを尊重し、0（自動）なら CPU 数に応じて選ぶ。結果は最低でも 1 となる
// （旧 EffectiveConcurrency。「nil = 自動」の pointer-as-optional を「0 = 自動」の
// 素の int に置き換えた）。
func Concurrency(requested, repoCount int) int {
	limit := parallel.AutoLimit()
	if requested > 0 {
		limit = requested
	}
	return max(min(limit, repoCount), 1)
}
