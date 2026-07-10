package tui

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendFile(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
}

func poll(t *testing.T, tl *tailer) []string {
	t.Helper()
	lines, err := tl.poll()
	if err != nil {
		t.Fatal(err)
	}
	return lines
}

func TestTailerReadsIncrementally(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.log")
	writeFile(t, path, "one\ntwo\n")
	tl := newTailer(path)

	if got := poll(t, tl); !reflect.DeepEqual(got, []string{"one", "two"}) {
		t.Fatalf("initial poll = %v", got)
	}
	if got := poll(t, tl); got != nil {
		t.Fatalf("no growth must yield nothing, got %v", got)
	}
	appendFile(t, path, "three\n")
	if got := poll(t, tl); !reflect.DeepEqual(got, []string{"three"}) {
		t.Fatalf("incremental poll = %v", got)
	}
}

// 改行で終わっていない書きかけの行は保留され、完成した時点で 1 行として返る
// （サーバーが行を分割して write するケース）。
func TestTailerBuffersPartialLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.log")
	writeFile(t, path, "comp")
	tl := newTailer(path)

	if got := poll(t, tl); got != nil {
		t.Fatalf("partial line must be held back, got %v", got)
	}
	appendFile(t, path, "lete\n")
	if got := poll(t, tl); !reflect.DeepEqual(got, []string{"complete"}) {
		t.Fatalf("completed line = %v", got)
	}
}

// ファイルの縮小（SpawnDetached のローテートや手動トランケート）は先頭からの
// 読み直しになる。
func TestTailerResetsOnTruncation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.log")
	writeFile(t, path, "old-1\nold-2\nold-3\n")
	tl := newTailer(path)
	poll(t, tl)

	writeFile(t, path, "new\n")
	if got := poll(t, tl); !reflect.DeepEqual(got, []string{"new"}) {
		t.Fatalf("after truncation = %v", got)
	}
}

// まだ存在しないログはエラーではない（サーバー起動前）。作成されたら先頭から読む。
func TestTailerToleratesMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.log")
	tl := newTailer(path)

	if got := poll(t, tl); got != nil {
		t.Fatalf("missing file must yield nothing, got %v", got)
	}
	writeFile(t, path, "born\n")
	if got := poll(t, tl); !reflect.DeepEqual(got, []string{"born"}) {
		t.Fatalf("after creation = %v", got)
	}
}

// 巨大なログの初回読みは末尾 initialWindow バイトに制限され、途中から始まる
// 最初の断片行は捨てられる。
func TestTailerInitialWindowSkipsLeadingFragment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.log")
	long := strings.Repeat("x", 100) + "\n"
	var b strings.Builder
	for b.Len() < initialWindow*2 {
		b.WriteString(long)
	}
	b.WriteString("tail-marker\n")
	writeFile(t, path, b.String())

	tl := newTailer(path)
	got := poll(t, tl)
	if len(got) == 0 || got[len(got)-1] != "tail-marker" {
		t.Fatalf("must read up to the end, got %d lines", len(got))
	}
	// 読み出したのは末尾ウィンドウの範囲内だけであり、全行ではない。
	if max := initialWindow / len(long); len(got) > max+1 {
		t.Fatalf("read too much: %d lines", len(got))
	}
	for _, l := range got[:len(got)-1] {
		if l != strings.TrimSuffix(long, "\n") {
			t.Fatalf("fragment leaked into the result: %q", l)
		}
	}
}

// 削除→再作成の後は初回扱いに戻る（primed のリセット）。再作成ファイルが
// initialWindow を超えても、末尾ウィンドウだけを読む不変条件を保つ。
func TestTailerRePrimesAfterRecreation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.log")
	writeFile(t, path, "small\n")
	tl := newTailer(path)
	if got := poll(t, tl); !reflect.DeepEqual(got, []string{"small"}) {
		t.Fatalf("initial poll = %v", got)
	}

	// 削除 → poll でリセットが走る。
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := poll(t, tl); got != nil {
		t.Fatalf("missing file must yield nothing, got %v", got)
	}

	// initialWindow を超えるファイルとして再作成する。
	long := strings.Repeat("y", 100) + "\n"
	var b strings.Builder
	for b.Len() < initialWindow*2 {
		b.WriteString(long)
	}
	b.WriteString("tail-marker\n")
	writeFile(t, path, b.String())

	got := poll(t, tl)
	if len(got) == 0 || got[len(got)-1] != "tail-marker" {
		t.Fatalf("must read up to the end, got %d lines", len(got))
	}
	// primed がリセットされていないとファイル全体を読んでしまう。末尾ウィンドウの
	// 範囲に限定されていることを行数で確認する。
	if max := initialWindow / len(long); len(got) > max+1 {
		t.Fatalf("read too much after recreation: %d lines", len(got))
	}
}

// 初回ウィンドウの断片は poll をまたいでも捨て続けられる（dropping フィールドの持続）。
// 末尾 64KB 内に改行が無いファイルを開いたあと、断片の続き + 改行 + 通常行を追記しても、
// 断片は行として emit されず通常行だけが出る。
func TestTailerKeepsDroppingFragmentAcrossPolls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.log")
	// 改行を 1 つも含まない initialWindow 超のファイル。
	writeFile(t, path, strings.Repeat("z", initialWindow+1000))
	tl := newTailer(path)

	if got := poll(t, tl); got != nil {
		t.Fatalf("no newline in the window must yield nothing, got %v", got)
	}
	// 断片の続き（改行で閉じる）＋ 通常行を追記する。dropping が持続していれば断片は
	// 最初の '\n' まで捨てられ、通常行だけが残る。
	appendFile(t, path, "-fragment-tail\nnormal-line\n")
	if got := poll(t, tl); !reflect.DeepEqual(got, []string{"normal-line"}) {
		t.Fatalf("fragment must be dropped, only the normal line remains, got %v", got)
	}
}

func TestRingKeepsOnlyTail(t *testing.T) {
	r := newRing(3)
	r.push("1", "2")
	r.push("3", "4", "5")
	got := r.slice()
	if len(got) != 3 || got[0] != "3" || got[2] != "5" {
		t.Fatalf("ring = %+v", got)
	}
}
