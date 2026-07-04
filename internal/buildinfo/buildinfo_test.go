package buildinfo

import "testing"

// Version は常に非空を返す。ldflags 注入が無いテストビルドでは、モジュール情報
// またはフォールバック定数に解決される。
func TestVersionIsNeverEmpty(t *testing.T) {
	if Version() == "" {
		t.Fatal("Version() must not be empty")
	}
}

// ldflags 注入相当の値が設定されていれば、それが最優先で報告される。
func TestInjectedVersionWins(t *testing.T) {
	orig := version
	t.Cleanup(func() { version = orig })

	version = "v9.9.9-test"
	if got := Version(); got != "v9.9.9-test" {
		t.Fatalf("Version() = %q, want the injected value", got)
	}
}
