package create

import (
	"path/filepath"

	"github.com/vimrak-hal/worktree-integrator/internal/app/action"
	"github.com/vimrak-hal/worktree-integrator/internal/core/git/repo"
	"github.com/vimrak-hal/worktree-integrator/internal/core/git/worktree"
	"github.com/vimrak-hal/worktree-integrator/internal/core/wtenv"
)

// buildRequests は選択された各リポジトリにつき 1 つの Request を構築し、各
// リポジトリの worktree を <root>/<repo> に配置する。コピー計画は Request には
// 含まれない（コピーは post-create ステップとして copy.go が担う）。ベースブランチは
// cfg.BaseFor(r.Name) がリポジトリごとに解決する（--base / repos.<repo>.base /
// defaults.base / "auto" の優先順位）。
//
// base の解決に失敗したリポジトリ（[repos.<repo>].base が不正など）は Request を作らず、
// そのリポジトリの失敗（StatusFailed）として failures に載せる。これにより設定内の 1 つの
// 不正な base が他リポジトリの作成を巻き込まず、失敗はそのリポジトリ単位に留まる。返す
// reqs は worktree.Run の結果とインデックスが一致し（順序保存）、failures は呼び出し側が
// 結果集約へ合流させる。
func buildRequests(cfg action.Create, root string, selected []repo.Repo) ([]worktree.Request, []worktree.Outcome) {
	var reqs []worktree.Request
	var failures []worktree.Outcome
	for _, r := range selected {
		base, err := cfg.BaseFor(r.Name)
		if err != nil {
			// base は fetch の位置引数（ブランチ名）になるため、その検証失敗は
			// FetchRef 段の失敗（多層防御が本来弾く段）として集約する。
			failures = append(failures, worktree.Outcome{
				Repo:   r.Name,
				Status: worktree.StatusFailed,
				Stage:  worktree.StageFetch,
				Err:    err,
			})
			continue
		}
		reqs = append(reqs, worktree.Request{
			RepoName:     r.Name,
			RepoPath:     r.Path,
			WorktreeName: cfg.WorktreeName.String(),
			Target:       filepath.Join(root, r.Name),
			Remote:       cfg.Remote,
			Base:         base,
		})
	}
	return reqs, failures
}

// createdRepoNames は worktree が実際に新規作成されたリポジトリ名を（リクエスト順で）
// 返す。setup 記録の無効化（create → server 状態の同期）が対象とする集合である。
func createdRepoNames(reqs []worktree.Request, results []worktree.Outcome) []string {
	var names []string
	for _, req := range reqs {
		for _, o := range results {
			if o.Repo == req.RepoName && o.Status == worktree.StatusCreated {
				names = append(names, req.RepoName)
				break
			}
		}
	}
	return names
}

// createdContexts は worktree が新規に作成された各リポジトリの RepoContext を、
// リクエスト順で構築する。スキップ・失敗したリポジトリには使用可能なチェックアウトが
// 無いため除外される。結果は after_worktree フックを実行すべき集合とちょうど一致する。
func createdContexts(reqs []worktree.Request, results []worktree.Outcome) []wtenv.RepoContext {
	var ctxs []wtenv.RepoContext
	for _, req := range reqs {
		for _, o := range results {
			if o.Repo == req.RepoName && o.Status == worktree.StatusCreated {
				ctxs = append(ctxs, wtenv.RepoContext{
					RepoName:     req.RepoName,
					RepoPath:     req.RepoPath,
					WorktreePath: req.Target,
				})
				break
			}
		}
	}
	return ctxs
}
