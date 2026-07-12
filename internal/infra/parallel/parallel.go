// Package parallel は、有界・順序保存・キャンセル対応の並列実行を共通化する純機構
// パッケージである。worktree の並列作成とフックの並列実行が個別に持っていた
// 「有界セマフォ + WaitGroup」の骨格を Map に一本化する。
//
// errgroup を採らないのは、「1 つの失敗で全体を止めない」「結果を全件集める」という
// 両者の要件に合わないためである。失敗は戻り値 R に畳み込んで表現する。
package parallel

import (
	"context"
	"runtime"
	"sync"
)

// Map は items の各要素へ fn を、同時に高々 limit 個のワーカーで適用し、結果を
// items と同じ並び（インデックス）で返す。1 つの fn が失敗しても他は止まらない
// （失敗は戻り値 R に含めて表現する設計）。fn は複数の goroutine から並行して
// 呼ばれるため、共有状態へ触れるなら fn 側で同期すること。
//
// limit が 1 未満なら 1 に丸める（無制限にはしない）。
//
// ctx がキャンセルされると、以降まだワーカーへ割り当てていない要素にはワーカーの
// 空きを待たせず、キャンセル済みの ctx を渡してその場で fn を呼ぶ。fn は ctx.Err()
// を見てスキップ結果（あるいは中断結果）を R として返せる。既に走り出したワーカーは
// そのまま完了する（キャンセルにどう応じるかは fn 次第）。
func Map[T, R any](ctx context.Context, limit int, items []T, fn func(ctx context.Context, i int, item T) R) []R {
	if limit < 1 {
		limit = 1
	}
	results := make([]R, len(items))
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i := range items {
		// キャンセル後は新規にワーカーへ割り当てない。セマフォの空き待ちの間の
		// キャンセルにも応答し、その場で（キャンセル済みの ctx で）fn を呼ぶ。
		select {
		case <-ctx.Done():
			results[i] = fn(ctx, i, items[i])
			continue
		case sem <- struct{}{}:
		}
		// select は両ケースが同時に成立するとランダムに選ぶため、セマフォを取れた
		// 場合でもキャンセル済みなら割り当てない。
		if ctx.Err() != nil {
			<-sem
			results[i] = fn(ctx, i, items[i])
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = fn(ctx, i, items[i])
		}(i)
	}
	wg.Wait()
	return results
}

// AutoLimit は並列度の自動的な上限である。リポジトリ単位・フック単位の作業は CPU
// よりもネットワークやディスク I/O、子プロセスの待ちが支配的なため、CPU 数に応じて
// スケールしつつ、妥当な 4..16 の範囲に収める。項目数との min は呼び出し側で行う。
func AutoLimit() int {
	return min(max(runtime.NumCPU()*4, 4), 16)
}
