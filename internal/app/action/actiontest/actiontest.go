// Package actiontest は action 型を扱うテスト向けの共通ヘルパーを提供する。検証済みの
// action.Name をテストで手早く構築するためのラッパー（ParseName の失敗をその場で
// t.Fatal する）を 1 か所に集約し、各テストパッケージでの重複を無くす。
package actiontest

import (
	"testing"

	"github.com/vimrak-hal/worktree-integrator/internal/app/action"
)

// MustName は raw を action.ParseName で検証して Name を返す。検証に失敗した場合は
// テストを失敗させる（呼び出し側は常に検証済みの名前を前提にできる）。testing.TB を
// 受けるため *testing.T でも *testing.B でも使える。
func MustName(t testing.TB, raw string) action.Name {
	t.Helper()
	n, err := action.ParseName(raw)
	if err != nil {
		t.Fatalf("ParseName(%q) = %v", raw, err)
	}
	return n
}
