package render

import (
	"fmt"
	"io"

	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
)

// ConfigCheck は `wt config check` の判定結果を描画する。見つからない・正常は案内を
// w へ書いて nil を返し、不正は検証エラーをそのまま返す（呼び出し元が stderr へ書き、
// exit 1 へ写す）。判定ロジックは core/config.Check が持ち、ここは文言だけを担う。
func ConfigCheck(w io.Writer, res config.CheckResult) error {
	switch res.Status {
	case config.CheckMissing:
		fmt.Fprintf(w, "設定ファイルがありません（%s）。既定値で動作します\n", res.Path)
		return nil
	case config.CheckOK:
		fmt.Fprintf(w, "設定は正常です（%s）\n", res.Path)
		return nil
	case config.CheckInvalid:
		return res.Err
	default:
		panic(fmt.Sprintf("unknown config.CheckStatus %d", res.Status))
	}
}
