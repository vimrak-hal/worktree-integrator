package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// Check は不存在・正常・不正の 3 通りを言語非依存の CheckResult で返す（文言と終了
// コードへの写像は表示層と main が担う）。
func TestCheck(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "nope.toml")
		res := Check(path)
		if res.Status != CheckMissing || res.Err != nil || res.Path != path {
			t.Fatalf("res = %+v", res)
		}
	})
	t.Run("ok", func(t *testing.T) {
		path := writeConfig(t, "remote = \"origin\"\n")
		res := Check(path)
		if res.Status != CheckOK || res.Err != nil || res.Path != path {
			t.Fatalf("res = %+v", res)
		}
	})
	t.Run("invalid", func(t *testing.T) {
		path := writeConfig(t, "[[hooks.before]]\ncommand = \"x\"\n")
		res := Check(path)
		if res.Status != CheckInvalid || res.Err == nil {
			t.Fatalf("res = %+v", res)
		}
		if !strings.Contains(res.Err.Error(), "is missing its `name`") {
			t.Fatalf("err = %v", res.Err)
		}
	})
}
