package config

import (
	"reflect"
	"testing"
)

func TestCopySpecUnmarshalArrayShorthand(t *testing.T) {
	var c CopySpec
	if err := c.UnmarshalTOML([]any{".env", "backend/.env"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Paths, []string{".env", "backend/.env"}) {
		t.Fatalf("paths = %v", c.Paths)
	}
}

func TestCopySpecUnmarshalTableRejectsUnknownKey(t *testing.T) {
	var c CopySpec
	err := c.UnmarshalTOML(map[string]any{"nope": true})
	if err == nil {
		t.Fatal("unknown key should be rejected")
	}
}

func TestCopySpecUnmarshalExcludeDefaults(t *testing.T) {
	var c CopySpec
	if err := c.UnmarshalTOML(map[string]any{"exclude_defaults": false}); err != nil {
		t.Fatal(err)
	}
	if c.ExcludeDefaults == nil || *c.ExcludeDefaults {
		t.Fatalf("ExcludeDefaults = %v, want explicit false", c.ExcludeDefaults)
	}
}

func TestCopySpecValidateRejectsEmptyEntries(t *testing.T) {
	if err := (CopySpec{Paths: []string{""}}).Validate(); err == nil {
		t.Fatal("empty path entry should be rejected")
	}
	if err := (CopySpec{Gitignored: true, Exclude: []string{""}}).Validate(); err == nil {
		t.Fatal("empty exclude entry should be rejected")
	}
	if err := (CopySpec{Paths: []string{".env"}}).Validate(); err != nil {
		t.Fatalf("valid spec should pass: %v", err)
	}
}

// exclude_defaults の解決順序: repo が明示すればそれを、repo が省略していれば
// defaults の明示を、どちらも省略なら既定で適用（安全側）を採用する。
func TestMergeCopyExcludeDefaultsResolutionOrder(t *testing.T) {
	no := false
	yes := true

	// repo が明示的に false → オプトアウト（defaults が何であれ）。
	plan := MergeCopy(CopySpec{Gitignored: true}, CopySpec{Gitignored: true, ExcludeDefaults: &no})
	if len(plan.Exclude) != 0 {
		t.Fatalf("repo explicit false should opt out: %v", plan.Exclude)
	}

	// repo 未指定・defaults が false → オプトアウト。
	plan = MergeCopy(CopySpec{Gitignored: true, ExcludeDefaults: &no}, CopySpec{Gitignored: true})
	if len(plan.Exclude) != 0 {
		t.Fatalf("defaults false should opt out when repo is silent: %v", plan.Exclude)
	}

	// repo が明示的に true で defaults が false → repo が勝ち、既定除外が入る。
	plan = MergeCopy(CopySpec{Gitignored: true, ExcludeDefaults: &no}, CopySpec{Gitignored: true, ExcludeDefaults: &yes})
	if len(plan.Exclude) == 0 {
		t.Fatalf("repo explicit true should win over defaults false: %v", plan.Exclude)
	}

	// どちらも未指定 → 既定で適用。
	plan = MergeCopy(CopySpec{Gitignored: true}, CopySpec{})
	if len(plan.Exclude) == 0 {
		t.Fatal("built-in defaults should apply when neither level opts out")
	}
}

func TestCopyPlanIsEmpty(t *testing.T) {
	if !(CopyPlan{}).IsEmpty() {
		t.Fatal("zero CopyPlan should be empty")
	}
	if (CopyPlan{Gitignored: true}).IsEmpty() {
		t.Fatal("gitignored plan should not be empty")
	}
	if (CopyPlan{Paths: []string{".env"}}).IsEmpty() {
		t.Fatal("plan with explicit paths should not be empty")
	}
}
