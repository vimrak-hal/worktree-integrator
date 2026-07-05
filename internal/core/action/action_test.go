package action

import (
	"strings"
	"testing"
)

// mustName はテスト用に検証済みの Name を構築する。
func mustName(t *testing.T, raw string) Name {
	t.Helper()
	n, err := ParseName(raw)
	if err != nil {
		t.Fatalf("ParseName(%q) = %v", raw, err)
	}
	return n
}

// ParseName は git check-ref-format 準拠に緩和された規則で名前を受理する。
// 旧 ValidateName では拒否されていた '_' と '.'（feat_login / v1.2）が合法になった
// ことを含めて固定する（意図的な仕様変更）。
func TestParseNameAcceptsGitRefSafeNames(t *testing.T) {
	ok := []string{
		"good-name123",
		"feature/login",
		"a/b/c-1",
		// 検証緩和で新たに合法になった名前。
		"feat_login",
		"v1.2",
		"release/v1.2.3",
		"a_b-c.d",
	}
	for _, n := range ok {
		parsed, err := ParseName(n)
		if err != nil {
			t.Errorf("ParseName(%q) = %v, want ok", n, err)
			continue
		}
		if parsed.String() != n {
			t.Errorf("ParseName(%q).String() = %q, want the input unchanged", n, parsed.String())
		}
	}
}

// ParseName の拒否規則を網羅する。各ケースでエラーメッセージが「何が違反か」を
// 具体的に含むことも確認する。
func TestParseNameRejectsUnsafeNames(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantMsg string // エラーメッセージに含まれるべき理由の断片
	}{
		{"空文字列", "", "空です"},
		{"先頭スラッシュ", "/leading", "空のセグメント"},
		{"末尾スラッシュ", "trailing/", "空のセグメント"},
		{"連続スラッシュ", "double//slash", "空のセグメント"},
		{"ドットのみのセグメント", "a/./b", "パストラバーサル"},
		{"親ディレクトリ", "dots/../traversal", "パストラバーサル"},
		{"ドット2つだけ", "..", "パストラバーサル"},
		{"先頭ハイフン", "-foo", "'-'"},
		{"セグメント先頭ハイフン", "feat/-x", "'-'"},
		{"先頭ドット", ".hidden", "'.'"},
		{"セグメント先頭ドット", "feat/.x", "'.'"},
		{".lock で終わる", "a.lock", ".lock"},
		{"セグメントが .lock で終わる", "x/y.lock/z", ".lock"},
		{"空白", "bad name", "使用できない文字"},
		{"タブ", "bad\tname", "使用できない文字"},
		{"制御文字", "bad\x01name", "使用できない文字"},
		{"コロン", "a:b", "使用できない文字"},
		{"クエスチョン", "a?b", "使用できない文字"},
		{"アスタリスク", "a*b", "使用できない文字"},
		{"角括弧", "a[b", "使用できない文字"},
		{"バックスラッシュ", `a\b`, "使用できない文字"},
		{"チルダ", "a~b", "使用できない文字"},
		{"キャレット", "a^b", "使用できない文字"},
		{"アットマーク（@{ を含む）", "a@{b", "使用できない文字"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseName(tt.input)
			if err == nil {
				t.Fatalf("ParseName(%q) = ok, want error", tt.input)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("ParseName(%q) error = %q, want it to mention %q", tt.input, err, tt.wantMsg)
			}
		})
	}
}

// validateRepoName は ParseName と同じセグメント規則を 1 セグメント分だけ適用し、
// "/" を含む名前を拒否する。
func TestValidateRepoName(t *testing.T) {
	for _, ok := range []string{"api", "my-app", "app_2", "v1.2"} {
		if err := validateRepoName(ok); err != nil {
			t.Errorf("validateRepoName(%q) = %v, want ok", ok, err)
		}
	}
	for _, bad := range []string{"", "a/b", "..", "-x", ".hidden", "bad name", "x.lock"} {
		if err := validateRepoName(bad); err == nil {
			t.Errorf("validateRepoName(%q) = ok, want error", bad)
		}
	}
}

// Name はゼロ値で空文字列を返すが、ParseName 経由では決して空にならない。
func TestNameStringRoundTrip(t *testing.T) {
	n := mustName(t, "feature/login")
	if n.String() != "feature/login" {
		t.Fatalf("String() = %q", n.String())
	}
}
