package hooks

import "testing"

// TestConfigIsEmpty は Config.IsEmpty が全タイミング空のときだけ true を返し、
// いずれかのタイミングにフックがあれば false を返すことを検証する。
func TestConfigIsEmpty(t *testing.T) {
	one := []Hook{hook("h", "true")}
	tests := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"全て空", Config{}, true},
		{"Before のみ", Config{Before: one}, false},
		{"AfterWorktree のみ", Config{AfterWorktree: one}, false},
		{"After のみ", Config{After: one}, false},
		{"全て非空", Config{Before: one, AfterWorktree: one, After: one}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.IsEmpty(); got != tt.want {
				t.Fatalf("IsEmpty() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestOutcomeIsFatal は Outcome.IsFatal が StatusFailed のときだけ true を返し、
// それ以外の状態は致命的でないことを検証する。
func TestOutcomeIsFatal(t *testing.T) {
	tests := []struct {
		name   string
		status Status
		want   bool
	}{
		{"Succeeded", StatusSucceeded, false},
		{"Warned", StatusWarned, false},
		{"Failed", StatusFailed, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := Outcome{Name: "x", Status: tt.status}
			if got := o.IsFatal(); got != tt.want {
				t.Fatalf("IsFatal() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestAnyFatal は AnyFatal が、StatusFailed を 1 つでも含むスライスに対してのみ
// true を返すことを検証する。空・全て非致命のケースは false。
func TestAnyFatal(t *testing.T) {
	tests := []struct {
		name     string
		outcomes []Outcome
		want     bool
	}{
		{"空スライス", nil, false},
		{
			"全て非致命",
			[]Outcome{
				{Name: "a", Status: StatusSucceeded},
				{Name: "b", Status: StatusWarned},
			},
			false,
		},
		{
			"末尾に致命を含む",
			[]Outcome{
				{Name: "a", Status: StatusSucceeded},
				{Name: "b", Status: StatusFailed},
			},
			true,
		},
		{
			// 致命が末尾でなくても全体を走査して検出することを確認する。
			"先頭に致命を含む",
			[]Outcome{
				{Name: "a", Status: StatusFailed},
				{Name: "b", Status: StatusSucceeded},
			},
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AnyFatal(tt.outcomes); got != tt.want {
				t.Fatalf("AnyFatal() = %v, want %v", got, tt.want)
			}
		})
	}
}
