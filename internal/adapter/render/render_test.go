package render

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/vimrak-hal/worktree-integrator/internal/app"
	"github.com/vimrak-hal/worktree-integrator/internal/core/git/worktree"
)

// PadRight は width 未満ならスペースで右詰めし、width 以上ならそのまま返す。
// 日本語（マルチバイト）でもバイトではなくルーン単位で数えて整列する。
func TestPadRight(t *testing.T) {
	if got := PadRight("ab", 5); got != "ab   " {
		t.Fatalf("短い文字列のパディング: got %q", got)
	}
	if got := PadRight("hello", 5); got != "hello" {
		t.Fatalf("width 丁度は素通りのはず: got %q", got)
	}
	if got := PadRight("hello!", 5); got != "hello!" {
		t.Fatalf("width 超過は素通りのはず: got %q", got)
	}
	padded := PadRight("日本語", 5)
	if utf8.RuneCountInString(padded) != 5 {
		t.Fatalf("マルチバイトのルーン整列が崩れている: %q (runes %d)", padded, utf8.RuneCountInString(padded))
	}
	if !strings.HasPrefix(padded, "日本語") {
		t.Fatalf("元の文字列を保持していない: %q", padded)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("short", 25); got != "short" {
		t.Fatalf("got %q", got)
	}
	long := strings.Repeat("a", 40)
	tr := truncate(long, 25)
	if len([]rune(tr)) != 25 || !strings.HasSuffix(tr, "…") {
		t.Fatalf("truncated = %q (len %d)", tr, len([]rune(tr)))
	}
}

// JSON は整形済み（インデント付き・HTML エスケープなし）で書き出し、round-trip
// 可能である。
func TestJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := JSON(&buf, map[string]string{"key": "<value>"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "\"<value>\"") {
		t.Fatalf("HTML escaping should be disabled: %q", buf.String())
	}
	var decoded map[string]string
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if decoded["key"] != "<value>" {
		t.Fatalf("decoded = %+v", decoded)
	}
}

// Progress.Update は repo タグ付きの進捗ラベル行を書き出す（途中経過のみ）。
func TestProgressUpdate(t *testing.T) {
	var buf bytes.Buffer
	p := NewProgress(&buf)
	p.Update("app", worktree.ProgressFetching)
	p.Update("app", worktree.ProgressCreating)
	want := "  [app] fetch中\n  [app] 作成中\n"
	if got := buf.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// Progress.Event は型付きイベントを日本語の 1 行に描画する。
func TestProgressEvent(t *testing.T) {
	var buf bytes.Buffer
	p := NewProgress(&buf)
	p.Event("app", worktree.Note{Kind: worktree.NoteCopyRejected, Path: "../escape"})
	if got := buf.String(); got != "  [app] コピー対象をスキップ（不正なパス）: ../escape\n" {
		t.Fatalf("got %q", got)
	}
}

// Progress は並行する goroutine からの書き込みでも行が混ざらないように直列化する。
// 各行が完全な「  [repo] ...\n」の形を保つことを確認する。
func TestProgressSerializesConcurrentWrites(t *testing.T) {
	var buf bytes.Buffer
	p := NewProgress(&buf)
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			p.Update("r", worktree.ProgressCreating)
			p.Update("r", worktree.ProgressFetching)
		})
	}
	wg.Wait()

	lines := bytes.Split(bytes.TrimRight(buf.Bytes(), "\n"), []byte("\n"))
	if len(lines) != 100 {
		t.Fatalf("行数 = %d, want 100", len(lines))
	}
	for _, ln := range lines {
		s := string(ln)
		if s != "  [r] 作成中" && s != "  [r] fetch中" {
			t.Fatalf("行が破損している: %q", s)
		}
	}
}

// Repos は 0 件のとき探索先 dir 付きの「見つからない」メッセージを出す。
func TestReposEmpty(t *testing.T) {
	var buf bytes.Buffer
	Repos(&buf, &app.ReposResult{ReposDir: "/repos", Repos: []app.RepoInfo{}})
	if got := buf.String(); got != "リポジトリが見つかりません（/repos）\n" {
		t.Fatalf("got %q", got)
	}
}

// Repos は複数件のとき件数ヘッダーに続けて 1 行ずつ名前とパスを出す。
func TestReposMultiple(t *testing.T) {
	var buf bytes.Buffer
	Repos(&buf, &app.ReposResult{ReposDir: "/repos", Repos: []app.RepoInfo{
		{Name: "alpha", Path: "/repos/alpha"},
		{Name: "beta", Path: "/repos/beta"},
	}})
	out := buf.String()
	if !strings.Contains(out, "/repos に 2 件のリポジトリ:") {
		t.Fatalf("件数ヘッダーが無い: %q", out)
	}
	if !strings.Contains(out, "- alpha\t/repos/alpha\n") {
		t.Fatalf("1 行目が無い: %q", out)
	}
	if !strings.Contains(out, "- beta\t/repos/beta\n") {
		t.Fatalf("2 行目が無い: %q", out)
	}
}

// Aliases は名前順のテーブル、空なら案内。AliasSet / AliasRemoved は結果の 1 行。
func TestAliasRendering(t *testing.T) {
	var buf bytes.Buffer
	Aliases(&buf, &app.AliasesResult{Aliases: map[string]string{}})
	if got := buf.String(); got != "別名は登録されていません\n" {
		t.Fatalf("got %q", got)
	}

	buf.Reset()
	Aliases(&buf, &app.AliasesResult{Aliases: map[string]string{
		"feat-b": "second", "feat-a": "first",
	}})
	out := buf.String()
	if !strings.Contains(out, "feat-a") || !strings.Contains(out, "first") {
		t.Fatalf("got %q", out)
	}
	if strings.Index(out, "feat-a") > strings.Index(out, "feat-b") {
		t.Fatalf("名前順に整列されるべき: %q", out)
	}

	buf.Reset()
	AliasSet(&buf, "feat-a", "ABC-123")
	if got := buf.String(); got != "別名を設定しました: feat-a = ABC-123\n" {
		t.Fatalf("got %q", got)
	}

	buf.Reset()
	AliasRemoved(&buf, "feat-a", true)
	if got := buf.String(); got != "別名を削除しました: feat-a\n" {
		t.Fatalf("got %q", got)
	}
	buf.Reset()
	AliasRemoved(&buf, "feat-a", false)
	if got := buf.String(); got != "別名は登録されていません: feat-a\n" {
		t.Fatalf("got %q", got)
	}
}
