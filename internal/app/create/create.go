// Package create は worktree 作成ワークフローを担う。すなわち、候補リポジトリの探索、
// 操作対象の選択、worktree の並列作成、追加ファイルのコピー（post-create ステップ）、
// ライフサイクルフックの実行、そして型付き Result への集約である。
//
// 対象の選択は 3 つのモードを持ち、いずれも同じ Run を通る:
//
//   - 名前指定（cfg.Repos 非空）— CLI の --repo と MCP の repos。存在しない名前は
//     探索結果と照合してエラーにする（かつては黙って取り除かれ、「何もすることが
//     ない」という誤解を招く成功に化けていた）。
//   - 全リポジトリ（cfg.All）— CLI の --all。
//   - 対話選択（どちらも無指定）— Selector として注入されたプロンプトで選ぶ。
//
// create は冪等な差分作成である: worktree ルートが既に存在しても短絡せず、before
// フックは常に実行される。対話選択モードでは未作成のリポジトリだけが選択肢に
// 提示され（差分選択）、全リポジトリが作成済みなら「追加するリポジトリはありません」
// で正常終了する（after フックは実行される）。名前指定・--all では既存メンバーは
// Skipped として報告される。旧実装の「ルート存在で全スキップ + after フックのみ」
// というショートサーキットは廃止され、その遷移ユースケース（after フックだけを
// 実行する）は `enter` コマンド（app/tree）に切り出された。
//
// 対話的なリポジトリ選択は Deps.Selector として呼び出し元（App が main の CLI
// アダプタ実装を渡す）から注入される。これにより端末 I/O を app/core の外に保ちつつ、
// 決定的なスタンドインに差し替えてテストから実行できる。ワークフローは io.Writer に
// 一切書かず、途中経過は Deps.Reporter（型付きイベント）、最終結果は Result として
// 返す。整形（日本語のテキスト・JSON）は adapter/render が担う。
package create

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/vimrak-hal/worktree-integrator/internal/core/action"
	"github.com/vimrak-hal/worktree-integrator/internal/core/git"
	"github.com/vimrak-hal/worktree-integrator/internal/core/git/repo"
	"github.com/vimrak-hal/worktree-integrator/internal/core/git/worktree"
	"github.com/vimrak-hal/worktree-integrator/internal/core/hooks"
	coreserver "github.com/vimrak-hal/worktree-integrator/internal/core/server"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/childio"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/fscopy"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/statedir"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/wtenv"
)

// Selector は探索されたリポジトリのうち操作対象とするものを選ぶ。空のスライス
// （error が nil）を返すと「何もしない」を意味する。対話プロンプトの中断
// （Ctrl-C）は空選択と同一視せず、エラー（cli.ErrInterrupted）として返すこと。
type Selector func(repos []repo.Repo) ([]repo.Repo, error)

// Deps は create ワークフローの依存の束。App が自身のフィールドから構築する。
type Deps struct {
	// ChildIO はフックの子プロセスに与える標準ストリーム。
	ChildIO childio.Streams
	// Selector は対話選択モードのリポジトリ選択。nil は非対話（MCP など）を意味し、
	// 対話選択モードに入るとエラーになる。
	Selector Selector
	// Root は状態ルート。新規作成した worktree に対する古い setup 記録の無効化に使う。
	Root statedir.Root
	// Reporter は途中経過（並列作成の進捗・コピーのイベント）の通知先。nil は無通知。
	Reporter worktree.Reporter
}

// reporter は Deps.Reporter を返し、nil なら無通知の実装に差し替える。
func (d Deps) reporter() worktree.Reporter {
	if d.Reporter == nil {
		return worktree.NopReporter{}
	}
	return d.Reporter
}

// Run は作成ワークフローを実行し、型付きの Result を返す。選択モードは
// cfg.Repos / cfg.All が決める（パッケージコメントを参照）。エラー時も、そこまでに
// 起きたこと（フックの結果・部分的な作成結果）を保持した Result を可能な限り返す。
// リポジトリの作成失敗とフックの失敗は errors.Join で併記され、どちらか一方が
// 他方を握り潰すことはない。
func Run(ctx context.Context, deps Deps, cfg action.Create) (*Result, error) {
	// 非対話（Selector 無し = 非 TTY / MCP）で対象も明示されていない呼び出しは、
	// フックや探索という副作用に触れる前に使い方エラーとして弾く。
	interactive := len(cfg.Repos) == 0 && !cfg.All
	if interactive && deps.Selector == nil {
		return nil, errors.New("対話的なリポジトリ選択が利用できません。--repo か --all を指定してください")
	}

	reporter := deps.reporter()
	// フックのコンテキストはすべてのタイミングで共有されるため、最初に一度だけ
	// 構築する。そのルート（<worktrees_dir>/<worktree_name>）は、すべての worktree
	// が作成される場所でもある。
	runCtx := wtenv.NewRunContext(cfg.WorktreeName.String(), cfg.ReposDir, cfg.WorktreesDir)
	res := &Result{
		Worktree:    cfg.WorktreeName.String(),
		Root:        runCtx.Root,
		ReposDir:    cfg.ReposDir,
		Disposition: DispositionCreated,
	}

	// "before" フックは事前チェックとして常に実行される（worktree ルートの有無に
	// よらない — 旧ショートサーキットの before スキップは廃止された）。ここでの
	// 失敗は、いずれのリポジトリにも触れる前に中断させる。
	before := hooks.Run(ctx, cfg.Hooks.Before, runCtx, deps.ChildIO)
	res.Hooks = appendHookOutcomes(res.Hooks, "before", before)
	if hooks.AnyFatal(before) {
		res.Disposition = DispositionAborted
		return res, errors.New("before フックが失敗したため中断します")
	}

	// 候補リポジトリを探索し、モードに従って操作対象を決める。
	repos, err := repo.Discover(ctx, cfg.ReposDir)
	if err != nil {
		return res, fmt.Errorf("リポジトリの探索に失敗しました（%s）: %w", cfg.ReposDir, err)
	}
	res.Discovered = len(repos)

	// 対話選択モードは差分作成: この worktree のメンバーとして既に存在する
	// リポジトリを選択肢から除く。全リポジトリが作成済みなら追加すべきものは
	// 無いが、after フック（作成完了/遷移フック）は実行して正常終了する。
	candidates := repos
	if interactive {
		candidates = withoutExistingMembers(runCtx.Root, repos)
		if len(repos) > 0 && len(candidates) == 0 {
			res.Disposition = DispositionNothingToAdd
			after := hooks.Run(ctx, cfg.Hooks.After, runCtx, deps.ChildIO)
			res.Hooks = appendHookOutcomes(res.Hooks, "after", after)
			if hooks.AnyFatal(after) {
				return res, errors.New("1 つ以上のフックが失敗しました")
			}
			return res, nil
		}
	}

	selected, err := pickRepositories(cfg, repos, candidates, deps.Selector)
	if err != nil {
		return res, err
	}
	if len(selected) == 0 {
		if len(repos) == 0 {
			res.Disposition = DispositionNoRepos
		} else {
			res.Disposition = DispositionNothingSelected
		}
		return res, nil
	}

	if err := os.MkdirAll(runCtx.Root, 0o755); err != nil {
		return res, fmt.Errorf("worktree ルート %s を作成できません: %w", runCtx.Root, err)
	}

	reqs := buildRequests(cfg, runCtx.Root, selected)
	concurrency := worktree.Concurrency(cfg.Concurrency, len(reqs))
	outcomes := worktree.Run(ctx, reqs, concurrency, reporter)

	// post-create ステップ: 設定された追加ファイル（例: gitignore された .env）を
	// 新しいチェックアウトへコピーする。コピーの部分失敗は Created を覆さず、
	// RepoOutcome.Copy レポートで区別される。キャンセル済みなら着手しない。
	copies := map[string]*fscopy.Report{}
	if ctx.Err() == nil {
		for i, o := range outcomes {
			if o.Status != worktree.StatusCreated {
				continue
			}
			req := reqs[i]
			copies[o.Repo] = copyExtras(ctx, req.RepoPath, req.Target, o.Repo, cfg.CopyPlanFor(o.Repo), reporter)
		}
	}
	res.Repos = repoOutcomes(outcomes, copies)
	for _, r := range res.Repos {
		switch r.Status {
		case RepoCreated:
			res.Created++
		case RepoSkipped:
			res.Skipped++
		case RepoFailed:
			res.Failed++
		}
	}

	// worktree を実際に新規作成したリポジトリでは、同名の worktree に対する古い
	// setup 記録（削除→同名再作成で残ったもの）を無効化する。作成自体は成功して
	// いるので、状態への書き込み失敗は警告（Result のフィールド）に留める。
	if err := invalidateSetupRecords(ctx, deps.Root, cfg.WorktreeName.String(), createdRepoNames(reqs, outcomes)); err != nil {
		res.SetupInvalidateError = err.Error()
	}

	// キャンセルされた実行は後続のフックに進まず、ここまでの結果だけをまとめて
	// ctx のエラーを返す（main が exit 130 へ写像する）。
	if err := ctx.Err(); err != nil {
		return res, err
	}

	// その場で中断しないフックの失敗を記録しておき、結果の集約後も非ゼロの終了
	// コードで実行を終えられるようにする。
	created := createdContexts(reqs, outcomes)
	afterWorktree := hooks.RunWorktree(ctx, cfg.Hooks.AfterWorktree, runCtx, created, deps.ChildIO)
	res.Hooks = appendHookOutcomes(res.Hooks, "after_worktree", afterWorktree)
	after := hooks.Run(ctx, cfg.Hooks.After, runCtx, deps.ChildIO)
	res.Hooks = appendHookOutcomes(res.Hooks, "after", after)

	// 失敗判定はワークフローの責務: リポジトリの失敗とフックの失敗を併記する
	// （旧実装はフック失敗のエラーが作成失敗のエラーを握り潰していた）。
	var errs []error
	if worktree.AnyFailed(outcomes) {
		errs = append(errs, fmt.Errorf("%d 件のリポジトリで worktree を作成できませんでした", res.Failed))
	}
	if hooks.AnyFatal(afterWorktree) || hooks.AnyFatal(after) {
		errs = append(errs, errors.New("1 つ以上のフックが失敗しました"))
	}
	return res, errors.Join(errs...)
}

// pickRepositories は探索済みのリポジトリから、選択モードに従って操作対象を決める。
// repos は探索された全リポジトリで、candidates は対話選択の選択肢（差分作成のため
// 既存メンバーが除かれている。名前指定・--all では repos と同じ — 既存メンバーは
// 作成段階で Skipped として報告される）。
//
// 名前指定モードでは、存在しない名前が 1 つでもあればエラーを返す（黙って取り除く
// と「何もすることがない」という誤解を招く成功に化けるため）。何もすることが無い
// 場合 — リポジトリが 1 つも見つからなかったか、対話選択で 1 つも選ばれなかった
// 場合 — は空のスライスを返し、Disposition の決定は呼び出し元が行う。
func pickRepositories(cfg action.Create, repos, candidates []repo.Repo, selector Selector) ([]repo.Repo, error) {
	// 名前指定モード。明示された名前を探索結果と照合し、実在するものだけを
	// 探索（ソート済み）順で処理する。
	if len(cfg.Repos) > 0 {
		if missing := repo.MissingNames(repos, cfg.Repos); len(missing) > 0 {
			return nil, fmt.Errorf("リポジトリ %s が見つかりません（repos_dir: %s）",
				quoteJoin(missing), cfg.ReposDir)
		}
		return repo.RetainNamed(repos, cfg.Repos), nil
	}

	if len(repos) == 0 {
		return nil, nil
	}

	// 全リポジトリモード。
	if cfg.All {
		return repos, nil
	}

	// 対話選択モード（Selector の存在は Run の冒頭で保証済み）。
	selected, err := selector(candidates)
	if err != nil {
		return nil, fmt.Errorf("リポジトリ選択: %w", err)
	}
	return selected, nil
}

// withoutExistingMembers は、root（worktree ルート）のメンバーとして既に存在する
// リポジトリ（<root>/<name> が .git を持つ）を除いた候補を返す。inventory と同じ
// 「.git を持つサブディレクトリ = メンバー」の判定であり、対話選択の差分作成が
// 未作成のリポジトリだけを提示するために使う。
func withoutExistingMembers(root string, repos []repo.Repo) []repo.Repo {
	var out []repo.Repo
	for _, r := range repos {
		if git.IsWorkTree(filepath.Join(root, r.Name)) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// quoteJoin は名前の一覧を `"a", "b"` の形の 1 つの文字列に整形する。
func quoteJoin(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = fmt.Sprintf("%q", n)
	}
	return strings.Join(quoted, ", ")
}

// repoOutcomes は worktree の結果と post-create コピーの結果を、リクエスト順の
// JSON 表現へ写す。コピーが部分失敗した Created は Stage="copy" で段階を示すが、
// Status は created のままである。
func repoOutcomes(outcomes []worktree.Outcome, copies map[string]*fscopy.Report) []RepoOutcome {
	out := make([]RepoOutcome, 0, len(outcomes))
	for _, o := range outcomes {
		ro := RepoOutcome{
			Repo:   o.Repo,
			Status: statusID(o.Status),
			Stage:  StageID(o.Stage),
			Copy:   copyReportDTO(copies[o.Repo]),
		}
		if o.Err != nil {
			ro.Error = o.Err.Error()
		}
		if ro.Status == RepoCreated && ro.Copy != nil && len(ro.Copy.Failures) > 0 {
			ro.Stage = StageID(worktree.StageCopy)
		}
		out = append(out, ro)
	}
	return out
}

// invalidateSetupRecords は、worktree を実際に新規作成した各リポジトリについて、
// 全サーバーの該当 worktree 名の setup 記録を状態から取り除く。同名の worktree を
// 削除して作り直した場合、記録が残っていると setup がスキップされてしまう
// （isFirst 判定は record.Path の実在も見るため二重の防御だが、こちらは記録自体を
// 正しく消す）。状態への書き込みは create の本務ではないため、エラーは呼び出し元が
// 警告に格下げする。
func invalidateSetupRecords(ctx context.Context, root statedir.Root, worktree string, created []string) error {
	if len(created) == 0 {
		return nil
	}
	store := coreserver.NewStateStore(root)
	return store.Update(ctx, func(state *coreserver.State) (bool, error) {
		dirty := false
		for _, repoName := range created {
			rs := state.Repos[repoName]
			if rs == nil {
				continue
			}
			for _, runtime := range rs.Servers {
				if runtime == nil {
					continue
				}
				if _, ok := runtime.Setup[worktree]; ok {
					delete(runtime.Setup, worktree)
					dirty = true
				}
			}
		}
		return dirty, nil
	})
}
