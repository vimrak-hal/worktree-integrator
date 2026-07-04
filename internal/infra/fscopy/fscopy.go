// Package fscopy は、ルート間で相対パス集合をコピーする純粋なコピーエンジンである。
// 「何をコピーするか」の語彙（設定スキーマ・リポジトリごとのプラン解決）は持たず、
// パスの集合と除外の集合を受け取って安全にコピーすることだけを担う（設定スキーマは
// core/config が所有する。core→infra の依存方向を守るため、excludes はここでは
// 単なる文字列パターンの集合として扱う）。
//
// excludes は gitignore 互換のパターンである（isExcludedRel を参照）: doublestar の
// "**" 展開に対応し、"/" を含むパターンはコピールートからの相対パスにアンカーし、
// 含まないパターンは深さを問わずベース名に照合する。末尾の "/" はディレクトリ限定を
// 意味する。
//
// エンジンはシンボリックリンク攻撃に対して堅牢化されている。絶対パス、".." による
// 脱出、".git" コンポーネント、"." のみのパスは拒否され、コピーはどちらの側でも
// 既存のシンボリックリンクを辿らない。".git" の拒否は大文字小文字（macOS などの
// case-insensitive ファイルシステム）と Unicode 正規化（NFC/NFD — HFS+ は NFD で
// 格納し、APFS は正規化非依存で照合する）の両方の迂回を塞ぐ。
//
// 中間ディレクトリの残留について: コピー先の中間ディレクトリ（descend が作成する）は
// リーフのコピーが失敗しても削除されない。部分的に成功したコピーの巻き戻しは行わず、
// 「作成済みの中間ディレクトリが残る」ことをこのエンジンの仕様とする（呼び出し側は
// Report.Failures で失敗を観測できる）。
package fscopy

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"golang.org/x/text/unicode/norm"
)

// Failure はコピーに失敗したパスを、元となったエラーとともに記録する。エラーは
// 型のまま保持し、文字列化は表示・シリアライズの時点で行う。
type Failure struct {
	Path string
	Err  error
}

// Report は 1 つのコピー先に対して paths をコピーした結果である。
type Report struct {
	// Copied はコピーに成功したパス（ソースルートからの相対パス）である。
	Copied []string
	// Failures はコピーに失敗したパスである。
	Failures []Failure
	// Rejected は安全でないとして拒否されたパス（絶対パス、".."、"."のみ、
	// または ".git" コンポーネントを含むもの）である。
	Rejected []string
}

// Merge は other を r に畳み込む（明示的なパスと gitignore のパスなど、複数回の
// CopyInto の結果を統合するために使う）。
func (r *Report) Merge(other Report) {
	r.Copied = append(r.Copied, other.Copied...)
	r.Failures = append(r.Failures, other.Failures...)
	r.Rejected = append(r.Rejected, other.Rejected...)
}

// CopyInto は paths の各パスを srcRoot から dstRoot へ、相対構造を保ったまま
// コピーする。
//
// 存在しないソースは黙ってスキップする。安全でないパスは拒否する。コピーは
// どちらの側でも既存のシンボリックリンクを辿らない。通常ファイルは内容をコピー
// し、ディレクトリは再帰的にコピーし、シンボリックリンクはそのまま再作成する。
// excludes は gitignore 互換のパターン（isExcludedRel を参照）で、トップレベルと
// 再帰の両方に適用される。
func CopyInto(srcRoot, dstRoot string, paths, excludes []string) Report {
	var report Report
	for _, rel := range paths {
		if !isSafeRelative(rel) {
			report.Rejected = append(report.Rejected, rel)
			continue
		}
		// トップレベルで除外する。除外は copyTree 内でも再帰的に適用される。
		if isExcludedTop(srcRoot, rel, excludes) {
			continue
		}
		copied, err := copyOne(srcRoot, dstRoot, rel, excludes)
		switch {
		case err != nil:
			report.Failures = append(report.Failures, Failure{Path: rel, Err: err})
		case copied:
			report.Copied = append(report.Copied, rel)
		}
	}
	return report
}

// isExcludedTop は paths 引数として渡されたトップレベルのエントリ（＝再帰的な
// ディレクトリ走査の外側にあり、fs.DirEntry を持たないもの）を除外すべきかを判定する。
// ディレクトリ限定パターン（末尾 "/"）の判定に実体の種別が要るため、srcRoot 側を
// Lstat する（シンボリックリンクは辿らず、ディレクトリとはみなさない）。
func isExcludedTop(srcRoot, rel string, excludes []string) bool {
	if len(excludes) == 0 {
		return false
	}
	isDir := false
	if info, err := os.Lstat(filepath.Join(srcRoot, rel)); err == nil {
		isDir = info.IsDir()
	}
	return isExcludedRel(rel, isDir, excludes)
}

// isExcludedRel は rel（コピールートからの相対パス）が excludes のいずれかに
// gitignore 互換の意味論でマッチするかを判定する。
//
//   - パターンが "/" を含む場合（先頭のみを含む場合を含む）はアンカー済みとして扱い、
//     コピールートからの相対パス全体を doublestar（"**" 対応）で照合する。
//   - パターンが "/" を含まない場合は非アンカーとして扱い、rel のベース名（末尾
//     コンポーネント）に照合する。呼び出し側が経路の各深さでこの関数を呼ぶため
//     （CopyInto のトップレベル呼び出しと copyTree の再帰呼び出し）、これだけで
//     「どの深さでもマッチしうる」という gitignore の非アンカー・パターンの意味論を
//     満たす。
//   - パターンが "/" で終わる場合はディレクトリ限定で、isDir が false なら常に不一致。
func isExcludedRel(rel string, isDir bool, excludes []string) bool {
	for _, pattern := range excludes {
		if matchesExcludePattern(rel, isDir, pattern) {
			return true
		}
	}
	return false
}

func matchesExcludePattern(rel string, isDir bool, pattern string) bool {
	dirOnly := strings.HasSuffix(pattern, "/")
	pattern = strings.TrimSuffix(pattern, "/")
	if pattern == "" {
		return false
	}
	if dirOnly && !isDir {
		return false
	}
	if strings.Contains(pattern, "/") {
		return doublestar.MatchUnvalidated(strings.TrimPrefix(pattern, "/"), rel)
	}
	return doublestar.MatchUnvalidated(pattern, path.Base(rel))
}

// copyOne は単一の rel エントリをコピーし、コピーした場合は (true, nil)、
// ソースが存在しなかった場合は (false, nil)、エラーの場合は（拒否された
// シンボリックリンクの辿りを含めて）エラーを返す。rel がディレクトリの場合、
// excludes は再帰的に適用される。
func copyOne(srcRoot, dstRoot, rel string, excludes []string) (bool, error) {
	src, err := descend(srcRoot, rel, false)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil // ソースの中間ディレクトリが存在しない — 黙ってスキップする
	}
	if err != nil {
		return false, err
	}
	// リーフ自体は正当に存在しないことがある（一部のリポジトリにしか存在しない）。
	if _, err := os.Lstat(src); err != nil {
		return false, nil
	}
	dst, err := descend(dstRoot, rel, true)
	if err != nil {
		return false, err
	}
	if err := copyTree(src, dst, rel, excludes); err != nil {
		return false, err
	}
	return true, nil
}

// descend は rel を root にコンポーネントごとに連結し、既存のシンボリックリンクの
// 中間要素を辿ることを拒否する。create が true の場合、存在しない中間ディレクトリは
// 実ディレクトリとして作成される（リーフのコピーが後で失敗しても、ここで作成した
// 中間ディレクトリは残る — パッケージコメントの「中間ディレクトリの残留」を参照）。
// リーフのパスを返す（存在チェックはしない）。create が false で中間ディレクトリが
// 存在しない場合は fs.ErrNotExist を返す。
func descend(root, rel string, create bool) (string, error) {
	comps := normalComponents(rel)
	cur := root
	for i, name := range comps {
		next := filepath.Join(cur, name)
		if i+1 == len(comps) {
			return next, nil // リーフ
		}
		info, err := os.Lstat(next)
		switch {
		case err == nil:
			if info.Mode()&fs.ModeSymlink != 0 {
				return "", fmt.Errorf("refusing to traverse symlinked directory: %s", next)
			}
			if !info.IsDir() {
				return "", fmt.Errorf("not a directory: %s", next)
			}
		case errors.Is(err, fs.ErrNotExist):
			if !create {
				return "", fmt.Errorf("missing intermediate directory: %w", fs.ErrNotExist)
			}
			if err := os.Mkdir(next, 0o755); err != nil {
				return "", err
			}
		default:
			return "", err
		}
		cur = next
	}
	return cur, nil // rel にコンポーネントがない場合のみ（事前に拒否済み）
}

// copyTree は既に安全に解決された src リーフを dst へ再帰的にコピーする。rel は
// コピールートからの相対パスである（すべての深さで excludes を適用するために使う）。
// シンボリックリンクはそのまま再作成し、決して辿らない。
func copyTree(src, dst, rel string, excludes []string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		if err := removeIfNotDir(dst); err != nil {
			return err
		}
		return os.Symlink(target, dst)
	case info.IsDir():
		dinfo, derr := os.Lstat(dst)
		switch {
		case derr == nil && dinfo.Mode()&fs.ModeSymlink != 0:
			return fmt.Errorf("refusing to copy into symlinked path: %s", dst)
		case derr == nil && dinfo.IsDir():
			// 既存のディレクトリを再利用する
		case derr == nil:
			return fmt.Errorf("destination exists and is not a directory: %s", dst)
		default:
			if err := os.Mkdir(dst, 0o755); err != nil {
				return err
			}
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			name := entry.Name()
			// 再帰中に現れた ".git"（大文字小文字・正規化の変種を含む）はコピー
			// しない。ネストしたリポジトリの git ディレクトリをコピー先の worktree に
			// 持ち込ませないための防御である。
			if isGitComponent(name) {
				continue
			}
			childRel := path.Join(rel, name)
			if isExcludedRel(childRel, entry.IsDir(), excludes) {
				continue
			}
			if err := copyTree(filepath.Join(src, name), filepath.Join(dst, name), childRel, excludes); err != nil {
				return err
			}
		}
		return nil
	default:
		// 通常ファイル: コピー先の既存のシンボリックリンクを通して書き込むことは決してしない。
		if err := removeIfNotDir(dst); err != nil {
			return err
		}
		return copyFileContents(src, dst, info.Mode().Perm())
	}
}

func copyFileContents(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// removeIfNotDir は dst がファイルまたはシンボリックリンクとして存在する場合に削除する
// （コピーで置き換えられるようにするため）。ディレクトリはそのまま残す
// （ディレクトリ対ディレクトリの処理は呼び出し側が行う）。
func removeIfNotDir(dst string) error {
	info, err := os.Lstat(dst)
	if err != nil {
		return nil
	}
	if !info.IsDir() {
		return os.Remove(dst)
	}
	return nil
}

// isGitComponent は名前が ".git" と同一視されうるかを返す。単純な等値ではなく
// strings.EqualFold（macOS などの case-insensitive ファイルシステムでは ".GIT" も
// 同じエントリになる）と Unicode 正規化（NFC/NFD — HFS+ は NFD で格納し、APFS は
// 正規化非依存で照合するため、正規化すると ".git" に一致する名前も同じエントリに
// なりうる）の両方で照合する。
func isGitComponent(name string) bool {
	return strings.EqualFold(norm.NFC.String(name), ".git") ||
		strings.EqualFold(norm.NFD.String(name), ".git")
}

// isSafeRelative は rel がコピーするのに安全な相対パスかを返す。すなわち、少なくとも
// 1つの実コンポーネントを持ち、絶対パスでなく、".." コンポーネントを含まず、".git"
// （大文字小文字・Unicode 正規化の変種を含む — isGitComponent を参照）をどの位置の
// コンポーネントにも含まないことを判定する。
func isSafeRelative(rel string) bool {
	if strings.HasPrefix(rel, "/") {
		return false // 絶対パス
	}
	comps := splitComponents(rel)
	hasNormal := false
	for _, c := range comps {
		switch c {
		case ".":
			// カレントディレクトリのマーカー、無害
		case "..":
			return false
		default:
			if isGitComponent(c) {
				return false
			}
			hasNormal = true
		}
	}
	return hasNormal
}

// splitComponents は相対パスを意味のあるセグメントに分割する。連続したスラッシュや
// 末尾のスラッシュは無視され、内部や末尾の "." は除去され、先頭の "." だけが
// （マーカーとして）残る（そのため "." のみのパスには実セグメントがない）。
// ".." は isSafeRelative が拒否できるよう保持される。
func splitComponents(rel string) []string {
	raw := strings.Split(rel, "/")
	out := make([]string, 0, len(raw))
	for i, seg := range raw {
		switch seg {
		case "":
			// 連続した／末尾のスラッシュ — 無視する
		case ".":
			if i == 0 {
				out = append(out, ".")
			}
		default:
			out = append(out, seg)
		}
	}
	return out
}

// normalComponents は rel の実パスセグメント（splitComponents から先頭の "." マーカーを
// 除いたもの）であり、実際にパスを連結するために使う。
func normalComponents(rel string) []string {
	out := splitComponents(rel)
	filtered := out[:0]
	for _, c := range out {
		if c != "." {
			filtered = append(filtered, c)
		}
	}
	return filtered
}
