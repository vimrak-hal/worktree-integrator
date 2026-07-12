// Package wtenv は、ワークフロー全体で共有される実行単位およびリポジトリ単位の
// コンテキストと、そこから導出される WT_* 環境変数を保持する。
//
// これらは WorktreeName・ReposDir・そこから導出される Root といったドメイン概念
// そのものであり、OS には依存しない。フック（core/hooks）とサーバー（core/server）の
// 両方が、シェルを介して実行するコマンドへこれらのパスを渡すため、コンテキスト型と
// WT_* 変数名の唯一の信頼できる定義は、いずれか一方のサブシステムに属するのではなく、
// 両者が共有するドメイン語彙として、共有元である core 配下のこのパッケージに置く。
package wtenv

import "path/filepath"

// RunContext は、すべてのフック（およびサーバーコマンド）で共有される実行単位の
// コンテキストであり、WT_* 環境変数として公開される。NewRunContext で構築することで
// Root が常に <WorktreesDir>/<WorktreeName> という導出値であることが保証される
// （呼び出し側ごとの filepath.Join の重複と、不整合な組み合わせを排除する）。
type RunContext struct {
	// WorktreeName はこの実行のワークツリー（およびブランチ）名。
	WorktreeName string
	// ReposDir はソースリポジトリを格納するベースディレクトリ。
	ReposDir string
	// WorktreesDir はワークツリーが作成されるベースディレクトリ。
	WorktreesDir string
	// Root はこの実行のワークツリールート: <WorktreesDir>/<WorktreeName>。
	Root string
}

// NewRunContext は実行単位のコンテキストを構築し、Root を
// <worktreesDir>/<worktreeName> として導出する。
func NewRunContext(worktreeName, reposDir, worktreesDir string) *RunContext {
	return &RunContext{
		WorktreeName: worktreeName,
		ReposDir:     reposDir,
		WorktreesDir: worktreesDir,
		Root:         filepath.Join(worktreesDir, worktreeName),
	}
}

// RepoContext は、リポジトリ単位のコンテキストであり、追加の WT_REPO_* /
// WT_WORKTREE_PATH 変数として公開される（after_worktree フックおよびサーバー
// コマンドで使われる）。
type RepoContext struct {
	// RepoName はリポジトリのディレクトリ名。
	RepoName string
	// RepoPath はソースリポジトリへのパス。
	RepoPath string
	// WorktreePath はこのリポジトリ用に新しく作成された（または現在アクティブな）
	// ワークツリーディレクトリ。
	WorktreePath string
}

// Pair は、単一の環境変数を名前／値のペアとして表す。
type Pair struct {
	Key   string
	Value string
}

// EnvPairs は、実行（および repo が非 nil の場合は特定リポジトリのワークツリー）を
// 記述する WT_* 環境変数を、名前／値のペアとして返す。契約は「どのキーがどの値を
// 持つか」であり、ペアの順序は実装詳細である（環境変数として渡る時点で順序は意味を
// 失うため、順序を契約に含めない）。
func EnvPairs(run *RunContext, repo *RepoContext) []Pair {
	env := []Pair{
		{"WT_WORKTREE_NAME", run.WorktreeName},
		{"WT_REPOS_DIR", run.ReposDir},
		{"WT_WORKTREES_DIR", run.WorktreesDir},
		{"WT_ROOT", run.Root},
	}
	if repo != nil {
		env = append(env,
			Pair{"WT_REPO_NAME", repo.RepoName},
			Pair{"WT_REPO_PATH", repo.RepoPath},
			Pair{"WT_WORKTREE_PATH", repo.WorktreePath},
		)
	}
	return env
}

// Environ は、pairs を exec.Cmd.Env 用の "KEY=VALUE" 文字列としてレンダリングし、
// base（通常は os.Environ()）に含まれる継承された環境に追加する。
func Environ(base []string, pairs []Pair) []string {
	out := make([]string, 0, len(base)+len(pairs))
	out = append(out, base...)
	for _, p := range pairs {
		out = append(out, p.Key+"="+p.Value)
	}
	return out
}
