package parallel

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// 完了順が入力順と食い違っても、結果はインデックス（リクエスト順）で集約される。
// 各要素の待ち時間を値に応じて変え、完了順を入力順とわざとずらす。
func TestMapPreservesRequestOrder(t *testing.T) {
	items := []int{5, 4, 3, 2, 1, 0}
	got := Map(context.Background(), 3, items, func(_ context.Context, _ int, v int) int {
		time.Sleep(time.Duration(v) * time.Millisecond)
		return v * 10
	})
	if len(got) != len(items) {
		t.Fatalf("len(got) = %d, want %d", len(got), len(items))
	}
	for i, v := range items {
		if got[i] != v*10 {
			t.Errorf("got[%d] = %d, want %d", i, got[i], v*10)
		}
	}
}

// 同時実行数は limit を超えない。十分な項目数と待ち時間を与え、実際に limit まで
// 到達する（直列化されていない）ことも併せて確かめる。
func TestMapRespectsLimit(t *testing.T) {
	const limit = 4
	var active, peak int32
	items := make([]int, 24)
	Map(context.Background(), limit, items, func(_ context.Context, _ int, _ int) int {
		cur := atomic.AddInt32(&active, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if cur <= p || atomic.CompareAndSwapInt32(&peak, p, cur) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&active, -1)
		return 0
	})
	if peak > limit {
		t.Fatalf("同時実行数 %d が上限 %d を超えた", peak, limit)
	}
	if peak != limit {
		t.Fatalf("上限まで並列化されなかった: peak = %d, want %d", peak, limit)
	}
}

// limit が 1 未満のときは無制限ではなく 1（直列）へ丸める。
func TestMapClampsNonPositiveLimitToSerial(t *testing.T) {
	var active, peak int32
	items := make([]int, 8)
	Map(context.Background(), 0, items, func(_ context.Context, _ int, _ int) int {
		cur := atomic.AddInt32(&active, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if cur <= p || atomic.CompareAndSwapInt32(&peak, p, cur) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		atomic.AddInt32(&active, -1)
		return 0
	})
	if peak != 1 {
		t.Fatalf("limit<=0 は 1（直列）へ丸めるはず。peak = %d", peak)
	}
}

// 既にキャンセル済みの ctx では、どの要素も着手（本処理）されず、fn は
// キャンセル済みの ctx を受け取ってスキップ結果を返す。fn は全要素について
// 呼ばれ（結果はリクエスト順で全件そろう）、本処理のカウンタは 0 のまま。
func TestMapSkipsUnstartedWorkWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var processed int32
	items := []int{0, 1, 2, 3, 4}
	got := Map(ctx, 2, items, func(ctx context.Context, _ int, _ int) string {
		if ctx.Err() != nil {
			return "skip"
		}
		atomic.AddInt32(&processed, 1)
		return "done"
	})
	if len(got) != len(items) {
		t.Fatalf("len(got) = %d, want %d", len(got), len(items))
	}
	for i, r := range got {
		if r != "skip" {
			t.Errorf("got[%d] = %q, want \"skip\"", i, r)
		}
	}
	if processed != 0 {
		t.Fatalf("キャンセル後に着手した項目がある: processed = %d", processed)
	}
}

// 1 つの失敗は他を止めない。奇数の項目を失敗させても、全項目の結果が
// リクエスト順で集約される。
func TestMapCollectsAllOutcomesIncludingFailures(t *testing.T) {
	items := []int{0, 1, 2, 3, 4, 5, 6, 7}
	got := Map(context.Background(), 3, items, func(_ context.Context, _ int, v int) error {
		if v%2 == 1 {
			return fmt.Errorf("boom %d", v)
		}
		return nil
	})
	if len(got) != len(items) {
		t.Fatalf("len(got) = %d, want %d", len(got), len(items))
	}
	for i, v := range items {
		switch {
		case v%2 == 1 && got[i] == nil:
			t.Errorf("項目 %d は失敗するはずだが nil", i)
		case v%2 == 0 && got[i] != nil:
			t.Errorf("項目 %d は成功するはずだが %v", i, got[i])
		}
	}
}

func TestAutoLimitStaysInBand(t *testing.T) {
	if l := AutoLimit(); l < 4 || l > 16 {
		t.Fatalf("AutoLimit() = %d, 4..16 の範囲外", l)
	}
}
