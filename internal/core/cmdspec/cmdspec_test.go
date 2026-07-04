package cmdspec

import "testing"

func TestFromStringScript(t *testing.T) {
	if got := FromString("npm install").Script(); got != "npm install" {
		t.Fatalf("script = %q", got)
	}
}

func TestUnmarshalSingleAndArray(t *testing.T) {
	var one Commands
	if err := one.UnmarshalTOML("npm install"); err != nil {
		t.Fatal(err)
	}
	if got := one.Script(); got != "npm install" {
		t.Fatalf("one script = %q", got)
	}

	var many Commands
	if err := many.UnmarshalTOML([]any{"npm ci", "npm run build"}); err != nil {
		t.Fatal(err)
	}
	if got := many.Script(); got != "npm ci && npm run build" {
		t.Fatalf("many script = %q", got)
	}
}

func TestUnmarshalRejectsNonString(t *testing.T) {
	var c Commands
	if err := c.UnmarshalTOML([]any{"ok", 5}); err == nil {
		t.Fatal("non-string element should error")
	}
	if err := (&c).UnmarshalTOML(42); err == nil {
		t.Fatal("non-string/array should error")
	}
}

// command = [] は設定ミスの可能性が高いため拒否する（(j) の検証要件）。
func TestUnmarshalRejectsEmptyArray(t *testing.T) {
	var c Commands
	if err := c.UnmarshalTOML([]any{}); err == nil {
		t.Fatal("empty array should error")
	}
}

// 空文字列の行（単一形式・配列要素のいずれも）は拒否する。
func TestUnmarshalRejectsEmptyStringLines(t *testing.T) {
	var c Commands
	if err := c.UnmarshalTOML(""); err == nil {
		t.Fatal("empty single string should error")
	}
	if err := c.UnmarshalTOML([]any{"ok", ""}); err == nil {
		t.Fatal("empty string element should error")
	}
}

func TestIsEmpty(t *testing.T) {
	if !(Commands{}).IsEmpty() {
		t.Fatal("zero Commands should be empty")
	}
	// FromString は UnmarshalTOML を経由しないプログラム的な構築のため、空文字列も
	// 受理する。IsEmpty は「キー省略」と「明示的な空文字列」を区別する。
	if FromString("").IsEmpty() {
		t.Fatal("explicit empty string command is not absent")
	}
}
