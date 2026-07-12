// Package inventory は worktrees_dir の実体スキャンを担う。「どの worktree セットが
// 存在し、それぞれにどのリポジトリのチェックアウトが入っているか」の真実源は
// ファイルシステムと git そのものであり、このパッケージはそれを列挙するだけで
// 永続的なマニフェストは持たない（設計判断: 真実源を増やさない）。ユーザーが
// `rm -rf` で worktree を消しても、次のスキャンがそのまま現実を報告する。
//
// list（Scan）と remove（Members）がこのスキャンを共有する。doctor と create は
// 別方式を採る: doctor は修復対象の名前を起点にルートの実在を検査し、create は
// 作成予定のリポジトリを git.IsWorkTree で差分判定するため、いずれも
// worktrees_dir 全体のスキャンを必要としない。
package inventory

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/vimrak-hal/worktree-integrator/internal/core/git"
)

// maxDepth はネストした worktree 名（"feature/login" など）を探すための再帰の
// 上限。worktree 名のセグメント数は実際には数個であり、worktrees_dir 配下の
// 無関係な深いディレクトリツリーを total スキャンしないための防波堤である。
const maxDepth = 8

// Worktree は worktrees_dir 配下で発見された 1 つの worktree セット。
type Worktree struct {
	// Name は worktrees_dir からの相対パス（= worktree 名。"feature/login" の
	// ようにセグメントを含みうる）。
	Name string `json:"name"`
	// Root は worktree セットのルートディレクトリ（<worktrees_dir>/<Name>）。
	Root string `json:"root"`
	// Repos は Root 直下の .git を持つサブディレクトリ（= リポジトリの
	// チェックアウト）。1 つも無い場合は空（rm -rf の残骸や空ディレクトリ）。
	Repos []RepoEntry `json:"repos,omitempty"`
}

// Broken はこのセットに壊れたチェックアウト（gitdir ポインタが死んでいる
// RepoEntry）が 1 つでも含まれるかを返す。
func (w Worktree) Broken() bool {
	for _, r := range w.Repos {
		if !r.Healthy {
			return true
		}
	}
	return false
}

// RepoEntry は worktree セット内の 1 つのリポジトリのチェックアウト。
type RepoEntry struct {
	// Repo はリポジトリのディレクトリ名（worktree ルート直下のサブディレクトリ名）。
	Repo string `json:"repo"`
	// Path はチェックアウトの絶対パス。
	Path string `json:"path"`
	// Branch はチェックアウトされているブランチ名（取得できなかった場合は空）。
	Branch string `json:"branch,omitempty"`
	// Healthy は gitdir ポインタが生きているか（.git の指す先が実在し、ブランチを
	// 解決できるか）。ソースリポジトリ側の管理情報が消えた rm -rf 残骸などで false に
	// なり、doctor --fix の対象として報告される。
	Healthy bool `json:"healthy"`
}

// Scan は worktreesDir 配下の worktree セットを列挙する。worktreesDir が存在しない
// 場合は空のリストを返す（初回利用はエラーではない）。結果は Name 順。
//
// worktree 名は '/' を含みうる（"feature/login" は <worktreesDir>/feature/login に
// 作られる）ため、直下だけでなくネストしたディレクトリも探索する: あるディレクトリは
// 「直下に .git を持つ子が 1 つでもあれば」worktree セットとみなされ、それ以上は
// 降りない。.git を持つ子が無ければサブディレクトリへ再帰する。サブディレクトリも
// 無い worktreesDir 直下の空ディレクトリは、リポジトリ 0 件の worktree セットとして
// 報告する（作成途中・削除残骸を list / doctor から観測できるようにする）。
//
// 健全性検査は「gitdir の指す先が実在するか」までで、repos_dir 配下の既知
// リポジトリとの厳密な照合（既知リポジトリの .git/worktrees を指しているか）は
// 行わない。worktree の列挙自体はソースリポジトリの探索に依存しないため、
// worktreesDir 以外の引数は取らない。
func Scan(ctx context.Context, worktreesDir string) ([]Worktree, error) {
	entries, err := os.ReadDir(worktreesDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("ワークツリーディレクトリ %s を読み取れません: %w", worktreesDir, err)
	}
	var out []Worktree
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() {
			continue
		}
		sub, err := scanDir(ctx, worktreesDir, entry.Name(), 1)
		if err != nil {
			return nil, err
		}
		out = append(out, sub...)
	}
	return out, nil
}

// scanDir は <worktreesDir>/<name> を検査し、そこに根を持つ worktree セットを
// 返す（パッケージコメントの規則を参照）。name は worktreesDir からの相対パス
// （スラッシュ区切り）。
func scanDir(ctx context.Context, worktreesDir, name string, depth int) ([]Worktree, error) {
	root := filepath.Join(worktreesDir, filepath.FromSlash(name))
	members, subdirs, err := membersAndSubdirs(ctx, root)
	if err != nil {
		return nil, err
	}
	if len(members) > 0 {
		return []Worktree{{Name: name, Root: root, Repos: members}}, nil
	}
	if len(subdirs) > 0 && depth < maxDepth {
		var out []Worktree
		for _, sub := range subdirs {
			nested, err := scanDir(ctx, worktreesDir, name+"/"+sub, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, nested...)
		}
		if len(out) > 0 {
			return out, nil
		}
	}
	// .git を持つ子がどこにも無い。直下（depth 1）のディレクトリだけを空の
	// worktree セットとして報告し、深い位置の無関係なディレクトリは報告しない。
	if depth == 1 {
		return []Worktree{{Name: name, Root: root}}, nil
	}
	return nil, nil
}

// Members は root 直下の .git を持つサブディレクトリを RepoEntry として列挙する。
// remove のように対象の worktree ルートが確定している呼び出し側が、worktrees_dir
// 全体のスキャンなしにメンバーを得るための入口である。root が存在しなければ空。
func Members(ctx context.Context, root string) ([]RepoEntry, error) {
	members, _, err := membersAndSubdirs(ctx, root)
	return members, err
}

// membersAndSubdirs は root 直下を 1 階層だけ読み、.git を持つ子を RepoEntry として、
// 持たない子ディレクトリを名前のまま返す。
func membersAndSubdirs(ctx context.Context, root string) (members []RepoEntry, subdirs []string, err error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("ワークツリールート %s を読み取れません: %w", root, err)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if !git.IsWorkTree(path) {
			subdirs = append(subdirs, entry.Name())
			continue
		}
		members = append(members, inspect(ctx, entry.Name(), path))
	}
	return members, subdirs, nil
}

// inspect は 1 つのチェックアウトの健全性とブランチを調べる。gitdir ポインタが
// 死んでいる場合は git を起動せずに Healthy=false とし、生きていればブランチを
// 解決する（解決に失敗した場合も Healthy=false — 記録より現実を信じる）。
func inspect(ctx context.Context, name, path string) RepoEntry {
	entry := RepoEntry{Repo: name, Path: path}
	if !gitdirAlive(path) {
		return entry
	}
	branch, err := git.CurrentBranch(ctx, path)
	if err != nil {
		return entry
	}
	entry.Branch = branch
	entry.Healthy = true
	return entry
}

// gitdirAlive は path の ".git" エントリが生きているかを返す。ディレクトリ
// （通常のクローン）なら生きているとみなし、ファイル（連結ワークツリーの
// "gitdir:" ポインタ）ならその指す先が実在するかを検査する。ソースリポジトリを
// 消した・作り直した場合、ポインタの指す先（<repo>/.git/worktrees/<id>）が消えて
// いるため、ここで rm -rf 残骸を検出できる。
func gitdirAlive(path string) bool {
	gitEntry := filepath.Join(path, ".git")
	info, err := os.Stat(gitEntry)
	if err != nil {
		return false
	}
	if info.IsDir() {
		return true
	}
	data, err := os.ReadFile(gitEntry)
	if err != nil {
		return false
	}
	target, found := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !found {
		return false
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(path, target)
	}
	_, err = os.Stat(target)
	return err == nil
}
