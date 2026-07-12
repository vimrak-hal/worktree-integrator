// Package repo は、ベースディレクトリ配下の Git リポジトリを検出・選択する。
package repo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/vimrak-hal/worktree-integrator/internal/core/git"
)

// Repo は、検出された Git リポジトリを表す。
type Repo struct {
	// Name はリポジトリのディレクトリ名（ワークツリーのサブディレクトリ名として
	// 使われる）。
	Name string
	// Path はリポジトリのワーキングディレクトリへの絶対パス。
	Path string
}

// Discover は、baseDir の直下にある Git リポジトリを返す。
//
// 直接の子のみを検査する（再帰はしない）。ディレクトリが ".git" エントリを含む場合に
// Git リポジトリとみなされる。このエントリはディレクトリ（通常のクローン）の場合も
// ファイル（既にリンクされたワークツリー）の場合もある。結果は名前順にソートされる。
// ネットワークファイルシステム上の巨大なディレクトリでも走査を打ち切れるよう、
// ctx のキャンセルに応答する。
//
// 失敗（探索対象ディレクトリの読み取り不能・走査中のキャンセル）は探索の文脈と
// baseDir を添えて返すため、呼び出し側（app/create・app/tree・app）はそのまま返せば
// よく、逐語の整形を各所で複製しなくてよい。
func Discover(ctx context.Context, baseDir string) ([]Repo, error) {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return nil, discoverError(baseDir, err)
	}

	var repos []Repo
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, discoverError(baseDir, err)
		}
		if !entry.IsDir() {
			continue
		}
		repoPath := filepath.Join(baseDir, entry.Name())
		if !git.IsWorkTree(repoPath) {
			continue
		}
		repos = append(repos, Repo{Name: entry.Name(), Path: repoPath})
	}

	slices.SortFunc(repos, func(a, b Repo) int {
		switch {
		case a.Name < b.Name:
			return -1
		case a.Name > b.Name:
			return 1
		default:
			return 0
		}
	})
	return repos, nil
}

// discoverError は Discover の失敗を、探索の文脈と baseDir を添えて包む。呼び出し側
// 3 箇所（app/create・app/tree・app）で逐語複製されていた整形をここへ集約する。
// %w で元エラーを保つため、キャンセル由来（context.Canceled）の errors.Is 判別は
// 呼び出し側でそのまま効く。
func discoverError(baseDir string, err error) error {
	return fmt.Errorf("リポジトリの探索に失敗しました（%s）: %w", baseDir, err)
}

// RetainNamed は、名前が names に含まれるリポジトリを保持し、検出（ソート済み）順を
// 維持する。インタラクティブなプロンプトと、（プロンプトせず明示的なリストを選択する）
// MCP の作成フローの両方で共有される。
func RetainNamed(repos []Repo, names []string) []Repo {
	var out []Repo
	for _, r := range repos {
		if slices.Contains(names, r.Name) {
			out = append(out, r)
		}
	}
	return out
}

// MissingNames は、want のうち all に検出されたリポジトリ名として存在しないものを、
// want の順序を保って返す。明示的なリポジトリ指定（MCP の作成フローなど）を、誤解を
// 招く「何もすることがない」という成功に陥らせず、実在しない要求としてフロントエンドが
// 報告できるようにするための述語である。
func MissingNames(all []Repo, want []string) []string {
	have := make(map[string]struct{}, len(all))
	for _, r := range all {
		have[r.Name] = struct{}{}
	}
	var missing []string
	for _, name := range want {
		if _, ok := have[name]; !ok {
			missing = append(missing, name)
		}
	}
	return missing
}
