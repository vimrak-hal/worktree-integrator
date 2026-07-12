package fscopy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestIsExcludedRel は、非アンカー（"/" を含まない）・アンカー（"/" を含む）・
// ディレクトリ限定（末尾 "/"）の 3 種のパターンを gitignore 互換の意味論で
// マッチングすることを確認する。
func TestIsExcludedRel(t *testing.T) {
	cases := []struct {
		rel      string
		isDir    bool
		excludes []string
		want     bool
	}{
		// 非アンカー: ベース名に照合し、深さを問わない。
		{"node_modules", true, []string{"node_modules"}, true},
		{"frontend/node_modules", true, []string{"node_modules"}, true},
		{"data/secrets.json", false, []string{"node_modules"}, false},
		// アンカー（"/" を含む）: コピールートからの相対パス全体に照合する。
		{"data/_storage", true, []string{"data/_storage"}, true},
		{"other/_storage", true, []string{"data/_storage"}, false},
		// doublestar の "**" 展開。
		{"x.log", false, []string{"**/*.log"}, true},
		{"a/b/x.log", false, []string{"**/*.log"}, true},
		{"a/b/x.txt", false, []string{"**/*.log"}, false},
		// ディレクトリ限定（末尾 "/"）はファイルにはマッチしない。
		{"dir", true, []string{"dir/"}, true},
		{"dir", false, []string{"dir/"}, false},
	}
	for _, c := range cases {
		if got := isExcludedRel(c.rel, c.isDir, c.excludes); got != c.want {
			t.Errorf("isExcludedRel(%q, isDir=%v, %v) = %v, want %v", c.rel, c.isDir, c.excludes, got, c.want)
		}
	}
}

func TestCopiesFilesAndDirectories(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(src, ".env"), "SECRET=1\n")
	if err := os.MkdirAll(filepath.Join(src, "data", "default-user"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(src, "data", "default-user", "secrets.json"), "{}")

	report := CopyInto(context.Background(), src, dst, []string{".env", "data"}, nil)
	if len(report.Failures) != 0 || len(report.Rejected) != 0 {
		t.Fatalf("report = %+v", report)
	}
	if len(report.Copied) != 2 {
		t.Fatalf("copied = %v", report.Copied)
	}
	if got := readFile(t, filepath.Join(dst, ".env")); got != "SECRET=1\n" {
		t.Fatalf(".env = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dst, "data", "default-user", "secrets.json")); err != nil {
		t.Fatal("nested file missing")
	}
}

// TestCanceledContextStopsCopy は、キャンセル済みの ctx を渡すとコピーに着手せず、
// ctx.Err() が Failure として記録されることを確認する（大きなツリーのコピー中でも
// Ctrl-C を効かせるための穴埋め）。
func TestCanceledContextStopsCopy(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(src, ".env"), "SECRET=1\n")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	report := CopyInto(ctx, src, dst, []string{".env"}, nil)
	if len(report.Copied) != 0 {
		t.Fatalf("copied = %v, want none", report.Copied)
	}
	if len(report.Failures) != 1 {
		t.Fatalf("failures = %+v, want one canceled failure", report.Failures)
	}
	if !errors.Is(report.Failures[0].Err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", report.Failures[0].Err)
	}
	if _, err := os.Stat(filepath.Join(dst, ".env")); err == nil {
		t.Fatal("canceled copy must not write the destination")
	}
}

func TestNestedFileTargetParentCreated(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(src, "backend", ".env"), "X=1")

	report := CopyInto(context.Background(), src, dst, []string{"backend/.env"}, nil)
	if !reflect.DeepEqual(report.Copied, []string{"backend/.env"}) {
		t.Fatalf("copied = %v", report.Copied)
	}
	if _, err := os.Stat(filepath.Join(dst, "backend", ".env")); err != nil {
		t.Fatal("nested target missing")
	}
}

func TestMissingSourcesSkippedSilently(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	report := CopyInto(context.Background(), src, dst, []string{".env"}, nil)
	if len(report.Copied) != 0 || len(report.Failures) != 0 || len(report.Rejected) != 0 {
		t.Fatalf("report = %+v", report)
	}
}

func TestUnsafePathsRejected(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(src, "ok"), "x")
	report := CopyInto(context.Background(), src, dst, []string{
		"/etc/passwd", "../escape", ".git/config", ".", "./", "",
	}, nil)
	if len(report.Rejected) != 6 {
		t.Fatalf("rejected = %v", report.Rejected)
	}
	if len(report.Copied) != 0 {
		t.Fatalf("copied = %v", report.Copied)
	}
}

// ".git" の拒否は大文字小文字（macOS などの case-insensitive FS では ".GIT" も
// 同じエントリになる）・位置（ネストしたコンポーネント）・Unicode 正規化
// （NFC/NFD）の迂回を許さない。
func TestGitComponentRejectedCaseAndPosition(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	unsafe := []string{
		".GIT/config",
		".Git/hooks/pre-commit",
		".gIt",
		"sub/.git/config",
		"sub/.GIT/config",
	}
	report := CopyInto(context.Background(), src, dst, unsafe, nil)
	if len(report.Rejected) != len(unsafe) {
		t.Fatalf("rejected = %v, want all %d rejected", report.Rejected, len(unsafe))
	}
	if len(report.Copied) != 0 || len(report.Failures) != 0 {
		t.Fatalf("report = %+v", report)
	}
}

// isGitComponent は NFC / NFD いずれの正規化形でも ".git" と照合する（".git" は
// ASCII のため正規化は恒等だが、照合が正規化を通ること自体を固定する）。
func TestIsGitComponentNormalizes(t *testing.T) {
	for _, name := range []string{".git", ".GIT", ".Git", ".gIt"} {
		if !isGitComponent(name) {
			t.Errorf("isGitComponent(%q) = false, want true", name)
		}
	}
	for _, name := range []string{".github", "git", ".gitignore"} {
		if isGitComponent(name) {
			t.Errorf("isGitComponent(%q) = true, want false", name)
		}
	}
}

// ディレクトリ再帰中に現れた ".git"（の変種）はコピーされない（ネストした
// リポジトリの git ディレクトリを持ち込ませない）。
func TestRecursionSkipsNestedGitDirectories(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "vendor", "pkg", ".GIT"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(src, "vendor", "pkg", ".GIT", "config"), "x")
	mustWrite(t, filepath.Join(src, "vendor", "pkg", "main.go"), "package pkg")

	report := CopyInto(context.Background(), src, dst, []string{"vendor"}, nil)
	if len(report.Failures) != 0 {
		t.Fatalf("failures = %+v", report.Failures)
	}
	if _, err := os.Stat(filepath.Join(dst, "vendor", "pkg", "main.go")); err != nil {
		t.Fatal("wanted file missing")
	}
	if _, err := os.Stat(filepath.Join(dst, "vendor", "pkg", ".GIT")); err == nil {
		t.Fatal("nested .GIT must not be copied")
	}
}

func TestSymlinksRecreatedNotFollowed(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(src, "dir", "real.txt"), "hi")
	if err := os.Symlink("/etc", filepath.Join(src, "dir", "escape")); err != nil {
		t.Fatal(err)
	}

	report := CopyInto(context.Background(), src, dst, []string{"dir"}, nil)
	if len(report.Failures) != 0 {
		t.Fatalf("failures = %+v", report.Failures)
	}
	if got := readFile(t, filepath.Join(dst, "dir", "real.txt")); got != "hi" {
		t.Fatalf("real.txt = %q", got)
	}
	info, err := os.Lstat(filepath.Join(dst, "dir", "escape"))
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("escape should stay a symlink")
	}
}

func TestRefusesWriteThroughSymlinkedDstAncestor(t *testing.T) {
	src, dst, outside := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(src, "data", "conf"), "payload")
	if err := os.Symlink(outside, filepath.Join(dst, "data")); err != nil {
		t.Fatal(err)
	}

	report := CopyInto(context.Background(), src, dst, []string{"data/conf"}, nil)
	if len(report.Copied) != 0 || len(report.Failures) != 1 {
		t.Fatalf("report = %+v", report)
	}
	if _, err := os.Stat(filepath.Join(outside, "conf")); err == nil {
		t.Fatal("must not write outside")
	}
}

func TestRefusesReadThroughSymlinkedSrcAncestor(t *testing.T) {
	src, dst, outside := t.TempDir(), t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(outside, "secret"), "top-secret")
	if err := os.Symlink(outside, filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}

	report := CopyInto(context.Background(), src, dst, []string{"link/secret"}, nil)
	if len(report.Copied) != 0 || len(report.Failures) != 1 {
		t.Fatalf("report = %+v", report)
	}
	if _, err := os.Stat(filepath.Join(dst, "link", "secret")); err == nil {
		t.Fatal("must not read through symlink")
	}
}

func TestExcludesAppliedRecursively(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "data", "node_modules", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(src, "data", "keep.txt"), "keep")
	mustWrite(t, filepath.Join(src, "data", "node_modules", "pkg", "huge.bin"), "x")

	report := CopyInto(context.Background(), src, dst, []string{"data"}, []string{"node_modules"})
	if len(report.Failures) != 0 {
		t.Fatalf("failures = %+v", report.Failures)
	}
	if _, err := os.Stat(filepath.Join(dst, "data", "keep.txt")); err != nil {
		t.Fatal("wanted file missing")
	}
	if _, err := os.Stat(filepath.Join(dst, "data", "node_modules")); err == nil {
		t.Fatal("nested node_modules must be pruned")
	}
}

func TestMergeFoldsAllSlices(t *testing.T) {
	errA, errB := os.ErrPermission, os.ErrNotExist
	r := Report{
		Copied:   []string{".env"},
		Failures: []Failure{{Path: "a", Err: errA}},
		Rejected: []string{"../x"},
	}
	r.Merge(Report{
		Copied:   []string{"backend/.env"},
		Failures: []Failure{{Path: "b", Err: errB}},
		Rejected: []string{"/abs"},
	})
	if !reflect.DeepEqual(r.Copied, []string{".env", "backend/.env"}) {
		t.Fatalf("copied = %v", r.Copied)
	}
	if !reflect.DeepEqual(r.Rejected, []string{"../x", "/abs"}) {
		t.Fatalf("rejected = %v", r.Rejected)
	}
	want := []Failure{{Path: "a", Err: errA}, {Path: "b", Err: errB}}
	if !reflect.DeepEqual(r.Failures, want) {
		t.Fatalf("failures = %v", r.Failures)
	}
}

func TestMergeEmptyOtherIsNoOp(t *testing.T) {
	r := Report{Copied: []string{".env"}}
	r.Merge(Report{})
	if !reflect.DeepEqual(r.Copied, []string{".env"}) {
		t.Fatalf("copied = %v", r.Copied)
	}
	if r.Failures != nil || r.Rejected != nil {
		t.Fatalf("unexpected non-nil slices: %+v", r)
	}
}

func TestTopLevelExcludeSkipsEntirePath(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "node_modules", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(src, "node_modules", "pkg", "x.js"), "x")

	// トップレベルのパスそのものが除外にマッチする場合は丸ごとスキップされる。
	report := CopyInto(context.Background(), src, dst, []string{"node_modules"}, []string{"node_modules"})
	if len(report.Copied) != 0 || len(report.Failures) != 0 || len(report.Rejected) != 0 {
		t.Fatalf("report = %+v", report)
	}
	if _, err := os.Stat(filepath.Join(dst, "node_modules")); err == nil {
		t.Fatal("excluded top-level path must not be copied")
	}
}

func TestExcludeLongerThanPathDoesNotMatch(t *testing.T) {
	// アンカー済みパターンが対象パスより深い場合は一致しない
	// （doublestar の厳密一致: "data" は "data/_storage" にマッチしない）。
	if isExcludedRel("data", true, []string{"data/_storage"}) {
		t.Fatal("exclude longer than path must not match")
	}
}

// TestDirOnlyExcludeAppliedRecursively は、末尾 "/" のディレクトリ限定パターンが
// 再帰コピー中も正しく適用され、同名のファイルには適用されないことを確認する。
func TestDirOnlyExcludeAppliedRecursively(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "data", "cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(src, "data", "cache", "big.bin"), "x")
	mustWrite(t, filepath.Join(src, "data", "cache.txt"), "keep")

	report := CopyInto(context.Background(), src, dst, []string{"data"}, []string{"cache/"})
	if len(report.Failures) != 0 {
		t.Fatalf("failures = %+v", report.Failures)
	}
	if _, err := os.Stat(filepath.Join(dst, "data", "cache")); err == nil {
		t.Fatal("directory-only exclude must prune the cache directory")
	}
	if _, err := os.Stat(filepath.Join(dst, "data", "cache.txt")); err != nil {
		t.Fatal("cache.txt (not a directory) must still be copied")
	}
}

// TestDoublestarGlobExcludeAppliedRecursively は "**/*.log" のような doublestar
// パターンが、再帰コピー中の任意の深さのファイルに一致することを確認する。
func TestDoublestarGlobExcludeAppliedRecursively(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "logs", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(src, "logs", "sub", "debug.log"), "x")
	mustWrite(t, filepath.Join(src, "logs", "keep.txt"), "keep")

	report := CopyInto(context.Background(), src, dst, []string{"logs"}, []string{"**/*.log"})
	if len(report.Failures) != 0 {
		t.Fatalf("failures = %+v", report.Failures)
	}
	if _, err := os.Stat(filepath.Join(dst, "logs", "sub", "debug.log")); err == nil {
		t.Fatal("**/*.log must exclude nested log files")
	}
	if _, err := os.Stat(filepath.Join(dst, "logs", "keep.txt")); err != nil {
		t.Fatal("non-matching file must still be copied")
	}
}

func TestMissingIntermediateSourceDirSkipped(t *testing.T) {
	// ソースの中間ディレクトリが存在しない（リーフ以前で欠落）場合は黙ってスキップ。
	src, dst := t.TempDir(), t.TempDir()
	report := CopyInto(context.Background(), src, dst, []string{"backend/config/.env"}, nil)
	if len(report.Copied) != 0 || len(report.Failures) != 0 || len(report.Rejected) != 0 {
		t.Fatalf("report = %+v", report)
	}
}

func TestSourceIntermediateNotADirectory(t *testing.T) {
	// ソース側の中間要素が通常ファイルの場合は "not a directory" エラー。
	src, dst := t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(src, "backend"), "iam a file")

	report := CopyInto(context.Background(), src, dst, []string{"backend/.env"}, nil)
	if len(report.Copied) != 0 || len(report.Failures) != 1 {
		t.Fatalf("report = %+v", report)
	}
	if !strings.Contains(report.Failures[0].Err.Error(), "not a directory") {
		t.Fatalf("err = %q", report.Failures[0].Err)
	}
}

func TestDestIntermediateNotADirectory(t *testing.T) {
	// コピー先の中間要素が通常ファイルの場合も "not a directory" エラー。
	src, dst := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(src, "backend", ".env"), "X=1")
	mustWrite(t, filepath.Join(dst, "backend"), "blocking file")

	report := CopyInto(context.Background(), src, dst, []string{"backend/.env"}, nil)
	if len(report.Copied) != 0 || len(report.Failures) != 1 {
		t.Fatalf("report = %+v", report)
	}
	if !strings.Contains(report.Failures[0].Err.Error(), "not a directory") {
		t.Fatalf("err = %q", report.Failures[0].Err)
	}
}

func TestOverwritesExistingFile(t *testing.T) {
	// コピー先に既存の通常ファイルがあれば内容が上書きされる。
	src, dst := t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(src, ".env"), "NEW=1\n")
	mustWrite(t, filepath.Join(dst, ".env"), "OLD=stale\n")

	report := CopyInto(context.Background(), src, dst, []string{".env"}, nil)
	if !reflect.DeepEqual(report.Copied, []string{".env"}) {
		t.Fatalf("copied = %v", report.Copied)
	}
	if got := readFile(t, filepath.Join(dst, ".env")); got != "NEW=1\n" {
		t.Fatalf(".env = %q", got)
	}
}

func TestOverwritesExistingSymlinkWithFile(t *testing.T) {
	// コピー先の既存シンボリックリンクは（辿らずに）削除され通常ファイルで置き換えられる。
	src, dst, outside := t.TempDir(), t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(src, ".env"), "REAL=1\n")
	mustWrite(t, filepath.Join(outside, "victim"), "do-not-touch\n")
	if err := os.Symlink(filepath.Join(outside, "victim"), filepath.Join(dst, ".env")); err != nil {
		t.Fatal(err)
	}

	report := CopyInto(context.Background(), src, dst, []string{".env"}, nil)
	if !reflect.DeepEqual(report.Copied, []string{".env"}) {
		t.Fatalf("copied = %v", report.Copied)
	}
	info, err := os.Lstat(filepath.Join(dst, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("destination should be a regular file, not a symlink")
	}
	if got := readFile(t, filepath.Join(dst, ".env")); got != "REAL=1\n" {
		t.Fatalf(".env = %q", got)
	}
	// シンボリックリンクを辿って外部ファイルを書き換えていないこと。
	if got := readFile(t, filepath.Join(outside, "victim")); got != "do-not-touch\n" {
		t.Fatalf("victim was modified: %q", got)
	}
}

func TestRecreatesSymlinkOverExistingFile(t *testing.T) {
	// ソースがシンボリックリンクで、コピー先に既存の通常ファイルがある場合、
	// 既存ファイルは削除されシンボリックリンクが再作成される。
	src, dst := t.TempDir(), t.TempDir()
	if err := os.Symlink("target/path", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dst, "link"), "stale regular file")

	report := CopyInto(context.Background(), src, dst, []string{"link"}, nil)
	if !reflect.DeepEqual(report.Copied, []string{"link"}) {
		t.Fatalf("copied = %v", report.Copied)
	}
	info, err := os.Lstat(filepath.Join(dst, "link"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("destination should be a symlink")
	}
	target, err := os.Readlink(filepath.Join(dst, "link"))
	if err != nil || target != "target/path" {
		t.Fatalf("readlink = %q, %v", target, err)
	}
}

func TestRefusesToCopyDirIntoSymlinkedDst(t *testing.T) {
	// コピー先の同名要素がディレクトリへのシンボリックリンクの場合、上書きを拒否する。
	src, dst, outside := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(src, "data", "f"), "x")
	if err := os.Symlink(outside, filepath.Join(dst, "data")); err != nil {
		t.Fatal(err)
	}

	report := CopyInto(context.Background(), src, dst, []string{"data"}, nil)
	if len(report.Copied) != 0 || len(report.Failures) != 1 {
		t.Fatalf("report = %+v", report)
	}
	if !strings.Contains(report.Failures[0].Err.Error(), "refusing to copy into symlinked path") {
		t.Fatalf("err = %q", report.Failures[0].Err)
	}
	if _, err := os.Stat(filepath.Join(outside, "f")); err == nil {
		t.Fatal("must not write through symlinked dst dir")
	}
}

func TestDirSourceOverExistingFileDestination(t *testing.T) {
	// ソースがディレクトリでコピー先に同名の通常ファイルがある場合はエラー。
	src, dst := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(src, "data", "f"), "x")
	mustWrite(t, filepath.Join(dst, "data"), "i am a file")

	report := CopyInto(context.Background(), src, dst, []string{"data"}, nil)
	if len(report.Copied) != 0 || len(report.Failures) != 1 {
		t.Fatalf("report = %+v", report)
	}
	if !strings.Contains(report.Failures[0].Err.Error(), "destination exists and is not a directory") {
		t.Fatalf("err = %q", report.Failures[0].Err)
	}
}

func TestReusesExistingDestinationDirectory(t *testing.T) {
	// コピー先に既存ディレクトリがあれば再利用し、中身をマージする。
	src, dst := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(src, "data", "new.txt"), "new")
	if err := os.MkdirAll(filepath.Join(dst, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dst, "data", "existing.txt"), "kept")

	report := CopyInto(context.Background(), src, dst, []string{"data"}, nil)
	if !reflect.DeepEqual(report.Copied, []string{"data"}) {
		t.Fatalf("copied = %v", report.Copied)
	}
	if got := readFile(t, filepath.Join(dst, "data", "existing.txt")); got != "kept" {
		t.Fatalf("existing.txt = %q", got)
	}
	if got := readFile(t, filepath.Join(dst, "data", "new.txt")); got != "new" {
		t.Fatalf("new.txt = %q", got)
	}
}

// 中間ディレクトリの残留仕様: リーフのコピーが失敗しても、descend が作成した
// コピー先の中間ディレクトリは削除されない。
func TestIntermediateDirsRemainAfterLeafFailure(t *testing.T) {
	src, dst, outside := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	// リーフをディレクトリへのシンボリックリンク先の占有でエラーにする。
	if err := os.MkdirAll(filepath.Join(src, "a", "b", "leaf"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(src, "a", "b", "leaf", "f"), "x")
	if err := os.MkdirAll(filepath.Join(dst, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dst, "a", "b", "leaf")); err != nil {
		t.Fatal(err)
	}

	report := CopyInto(context.Background(), src, dst, []string{"a/b/leaf"}, nil)
	if len(report.Failures) != 1 {
		t.Fatalf("report = %+v", report)
	}
	// 失敗しても中間ディレクトリ dst/a/b は残る（仕様）。
	if _, err := os.Stat(filepath.Join(dst, "a", "b")); err != nil {
		t.Fatal("intermediate directories must remain after a leaf failure")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
