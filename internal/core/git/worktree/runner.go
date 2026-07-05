package worktree

import (
	"context"
	"runtime"
	"sync"
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
	if concurrency < 1 {
		concurrency = 1
	}
	results := make([]Outcome, len(reqs))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i := range reqs {
		// キャンセル後は新規に着手しない。セマフォの空き待ちの間のキャンセルにも
		// 応答する。
		select {
		case <-ctx.Done():
			results[i] = skipCanceled(reqs[i])
			continue
		case sem <- struct{}{}:
		}
		// select は両ケースが同時に成立するとランダムに選ぶため、セマフォを取れた
		// 場合でもキャンセル済みなら着手しない。
		if ctx.Err() != nil {
			<-sem
			results[i] = skipCanceled(reqs[i])
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = Process(ctx, reqs[i], reporter)
		}(i)
	}
	wg.Wait()
	return results
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
	limit := autoConcurrencyCap()
	if requested > 0 {
		limit = requested
	}
	return max(min(limit, repoCount), 1)
}

// autoConcurrencyCap は並列度の自動的な上限である。リポジトリ単位の作業は CPU
// よりもネットワークやディスク I/O が支配的なため、CPU 数に応じてスケールしつつ、
// 妥当な 4..16 の範囲に収める。
func autoConcurrencyCap() int {
	return min(max(runtime.NumCPU()*4, 4), 16)
}
