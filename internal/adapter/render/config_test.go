package render

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
)

// ConfigCheck は 3 種の判定を描画する: 不存在・正常は案内を書いて nil を返し、不正は
// 検証エラーをそのまま返して何も書かない。
func TestConfigCheck(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		var buf bytes.Buffer
		if err := ConfigCheck(&buf, config.CheckResult{Path: "/cfg.toml", Status: config.CheckMissing}); err != nil {
			t.Fatalf("err = %v", err)
		}
		out := buf.String()
		if !strings.Contains(out, "設定ファイルがありません（/cfg.toml）") || !strings.Contains(out, "既定値で動作します") {
			t.Fatalf("buf = %q", out)
		}
	})
	t.Run("ok", func(t *testing.T) {
		var buf bytes.Buffer
		if err := ConfigCheck(&buf, config.CheckResult{Path: "/cfg.toml", Status: config.CheckOK}); err != nil {
			t.Fatalf("err = %v", err)
		}
		if !strings.Contains(buf.String(), "設定は正常です（/cfg.toml）") {
			t.Fatalf("buf = %q", buf.String())
		}
	})
	t.Run("invalid はエラーを返し何も書かない", func(t *testing.T) {
		var buf bytes.Buffer
		want := errors.New("bad config")
		if err := ConfigCheck(&buf, config.CheckResult{Path: "/cfg.toml", Status: config.CheckInvalid, Err: want}); err != want {
			t.Fatalf("err = %v, want %v", err, want)
		}
		if buf.Len() != 0 {
			t.Fatalf("invalid は文言を書かない: %q", buf.String())
		}
	})
}
