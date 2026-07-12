package tree

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/vimrak-hal/worktree-integrator/internal/core/git"
	"github.com/vimrak-hal/worktree-integrator/internal/core/git/repo"
	coreserver "github.com/vimrak-hal/worktree-integrator/internal/core/server"
)

// DoctorResult は `doctor` の結果。
type DoctorResult struct {
	// Findings は全チェックの発見（チェックの実行順・チェック内は決定的な順序）。
	// 空でも非 nil（JSON / structuredContent で null にならない）。
	Findings []Finding `json:"findings"`
	// Fix は --fix モードで実行されたかどうか。
	Fix bool `json:"fix"`
	// Fixed / FixFailed は修復の集計（--fix のみ非ゼロになりうる）。
	Fixed     int `json:"fixed"`
	FixFailed int `json:"fix_failed"`
	// LegacyBackup は旧形式の状態ファイルを退避した先のパス（発生時のみ）。
	LegacyBackup string `json:"legacy_state_backup,omitempty"`
}

// SetLegacyBackup は旧形式状態ファイルの退避先を記録する（App が退避の有無を写す）。
func (r *DoctorResult) SetLegacyBackup(bak string) { r.LegacyBackup = bak }

// Finding は doctor が発見した 1 つの問題。どのフィールドが意味を持つかは Check に
// 依存する。ユーザー向けの文言は持たず、表示層（render）が Check をキーに日本語へ
// 変換する。
type Finding struct {
	// Check は発見したチェックの識別子（check.Name）。
	Check string `json:"check"`
	// Repo / Server / Worktree / Path は対象の座標（該当するもののみ）。
	Repo     string `json:"repo,omitempty"`
	Server   string `json:"server,omitempty"`
	Worktree string `json:"worktree,omitempty"`
	Path     string `json:"path,omitempty"`
	// Detail は補足の技術的詳細（pid・git の出力など。原文のまま）。
	Detail string `json:"detail,omitempty"`
	// Fixable は --fix で修復できる発見かどうか（false は報告のみ）。
	Fixable bool `json:"fixable"`
	// Fixed / FixError は --fix での修復の結果（--fix かつ Fixable のみ）。
	Fixed    bool   `json:"fixed,omitempty"`
	FixError string `json:"fix_error,omitempty"`
}

// Doctor は自己診断を実行し、fix なら修復可能な発見をその場で修復する。fix なしは
// 報告のみで何も変更しない。チェックの実行に失敗した場合と修復に失敗した場合は
// （そこまでの結果を保持した）Result とともに非 nil のエラーを返す。発見が
// あること自体はエラーではない（exit 0）。
func Doctor(ctx context.Context, d Deps, fix bool) (*DoctorResult, error) {
	repos, err := repo.Discover(ctx, d.ReposDir)
	if err != nil {
		return nil, fmt.Errorf("リポジトリの探索に失敗しました（%s）: %w", d.ReposDir, err)
	}
	env := &checkEnv{deps: d, repos: repos}

	res := &DoctorResult{Findings: []Finding{}, Fix: fix}
	var errs []error
	for _, c := range checks() {
		findings, err := c.Scan(ctx, env)
		if err != nil {
			errs = append(errs, fmt.Errorf("チェック %s: %w", c.Name(), err))
			continue
		}
		for i := range findings {
			f := &findings[i]
			f.Check = c.Name()
			if !fix || !f.Fixable {
				continue
			}
			if fixErr := c.Fix(ctx, env, *f); fixErr != nil {
				f.FixError = fixErr.Error()
				res.FixFailed++
				errs = append(errs, fmt.Errorf("修復 %s: %w", c.Name(), fixErr))
			} else {
				f.Fixed = true
				res.Fixed++
			}
		}
		res.Findings = append(res.Findings, findings...)
	}
	return res, errors.Join(errs...)
}

// checkEnv は全チェックが共有する実行環境。repos の探索を一度だけ行い、各チェックに
// 配る。worktree の実在確認は（inventory の全スキャンではなく）記録された名前ごとの
// ルート存在検査で行う — 記録をキーに現実を確認する方向のチェックには、それで
// 十分かつ正確である。
type checkEnv struct {
	deps  Deps
	repos []repo.Repo
}

// worktreeExists は name の worktree ルートが実在するかを返す。記録（setup・別名・
// ログ名）をファイルシステムの現実と突き合わせる各チェックの共通述語である。
func (e *checkEnv) worktreeExists(name string) bool {
	return isDir(filepath.Join(e.deps.WorktreesDir, name))
}

// check は doctor の 1 つの診断。チェックの追加 = この interface の実装型を 1 つ
// 書いて checks() の一覧に並べることであり、Doctor 本体には手を入れない。
type check interface {
	// Name は Finding.Check に入る識別子（表示層が日本語ラベルへ変換するキー）。
	Name() string
	// Scan は問題を列挙する。何も変更しない。
	Scan(ctx context.Context, env *checkEnv) ([]Finding, error)
	// Fix は Fixable な発見 1 件を修復する。現実が既に変わっていた（対象が消えて
	// いた等の）場合は何もせず成功として扱うこと。
	Fix(ctx context.Context, env *checkEnv, f Finding) error
}

// checks は実行順のチェック一覧。状態の掃除（1〜4）→ git の掃除（5）→ 報告のみの
// 整合性検査（6〜7）の順。
func checks() []check {
	return []check{
		deadRunning{},
		staleSetup{},
		staleAlias{},
		orphanLogs{},
		pruneWorktrees{},
		brokenRepos{},
		configWithoutRepo{},
		repoWithoutServers{},
	}
}

// ----- チェック 1: 稼働記録の生存照合 -----

// deadRunning は全 Running の生存を Ident（pid + 開始時刻）の照合で検証し、消滅
// しているのに記録が残っているものを報告する。--fix で記録をクリアする（status の
// Probe と同じ自己修復を、明示コマンドとしても提供する）。
type deadRunning struct{}

func (deadRunning) Name() string { return "dead_running" }

func (deadRunning) Scan(ctx context.Context, env *checkEnv) ([]Finding, error) {
	var out []Finding
	err := env.deps.Store.View(ctx, func(state *coreserver.State) error {
		for _, repoName := range slices.Sorted(maps.Keys(state.Repos)) {
			rs := state.Repos[repoName]
			for _, serverName := range slices.Sorted(maps.Keys(rs.Servers)) {
				runtime := rs.Servers[serverName]
				if runtime == nil || runtime.Running == nil {
					continue
				}
				if env.deps.Proc.Alive(runtime.Running.Ident) {
					continue
				}
				out = append(out, Finding{
					Repo:     repoName,
					Server:   serverName,
					Worktree: runtime.Running.Worktree,
					Detail:   fmt.Sprintf("pid %d", runtime.Running.Ident.Pid),
					Fixable:  true,
				})
			}
		}
		return nil
	})
	return out, err
}

func (deadRunning) Fix(ctx context.Context, env *checkEnv, f Finding) error {
	// ロック順序: repo 操作ロック → 状態ロック。修復の直前に生存を再確認し、
	// スキャン後に別プロセスが起動し直していた場合は触らない。
	return env.deps.Root.WithRepoLock(ctx, f.Repo, func() error {
		return env.deps.Store.Update(ctx, func(state *coreserver.State) (bool, error) {
			rs := state.Repos[f.Repo]
			if rs == nil {
				return false, nil
			}
			runtime := rs.Servers[f.Server]
			if runtime == nil || runtime.Running == nil || env.deps.Proc.Alive(runtime.Running.Ident) {
				return false, nil
			}
			runtime.Running = nil
			return true, nil
		})
	})
}

// ----- チェック 2: setup 記録の実在照合 -----

// staleSetup は Setup に記録された worktree 名の実在を確認し、ルートが消えている
// 記録を報告する。--fix で記録を取り除く（remove を経ずに rm -rf された worktree の
// 残骸掃除）。
type staleSetup struct{}

func (staleSetup) Name() string { return "stale_setup" }

func (staleSetup) Scan(ctx context.Context, env *checkEnv) ([]Finding, error) {
	var out []Finding
	err := env.deps.Store.View(ctx, func(state *coreserver.State) error {
		for _, repoName := range slices.Sorted(maps.Keys(state.Repos)) {
			rs := state.Repos[repoName]
			for _, serverName := range slices.Sorted(maps.Keys(rs.Servers)) {
				runtime := rs.Servers[serverName]
				if runtime == nil {
					continue
				}
				for _, worktree := range slices.Sorted(maps.Keys(runtime.Setup)) {
					if env.worktreeExists(worktree) {
						continue
					}
					out = append(out, Finding{
						Repo: repoName, Server: serverName, Worktree: worktree, Fixable: true,
					})
				}
			}
		}
		return nil
	})
	return out, err
}

func (staleSetup) Fix(ctx context.Context, env *checkEnv, f Finding) error {
	return env.deps.Root.WithRepoLock(ctx, f.Repo, func() error {
		return env.deps.Store.Update(ctx, func(state *coreserver.State) (bool, error) {
			rs := state.Repos[f.Repo]
			if rs == nil {
				return false, nil
			}
			runtime := rs.Servers[f.Server]
			if runtime == nil {
				return false, nil
			}
			if _, ok := runtime.Setup[f.Worktree]; !ok {
				return false, nil
			}
			delete(runtime.Setup, f.Worktree)
			return true, nil
		})
	})
}

// ----- チェック 3: 別名の実在照合 -----

// staleAlias は別名の各キーに対応する worktree ルートの実在を確認し、消えている
// ものを報告する。--fix で別名を削除する。
type staleAlias struct{}

func (staleAlias) Name() string { return "stale_alias" }

func (staleAlias) Scan(ctx context.Context, env *checkEnv) ([]Finding, error) {
	doc, err := env.deps.Aliases.Load(ctx)
	if err != nil {
		return nil, err
	}
	var out []Finding
	for _, name := range slices.Sorted(maps.Keys(doc.Aliases)) {
		if env.worktreeExists(name) {
			continue
		}
		out = append(out, Finding{Worktree: name, Detail: doc.Aliases[name], Fixable: true})
	}
	return out, nil
}

func (staleAlias) Fix(ctx context.Context, env *checkEnv, f Finding) error {
	_, err := env.deps.Aliases.Remove(ctx, f.Worktree)
	return err
}

// ----- チェック 4: 孤児ログ -----

// orphanLogs は logs/ 配下で、どこからも参照されないログファイルを報告する。
// 「参照されている」とは、state の Instance.Log / LastLog に記録されている
// （その .prev を含む）か、ファイル名が指す worktree が実在する（= 実在 worktree の
// 決定的 LogPath に該当する）こと。命名規則に従わないファイルには触れない。
// --fix でファイルを削除する。
type orphanLogs struct{}

func (orphanLogs) Name() string { return "orphan_log" }

func (orphanLogs) Scan(ctx context.Context, env *checkEnv) ([]Finding, error) {
	logsDir := env.deps.Root.LogsDir()
	entries, err := os.ReadDir(logsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	// state に記録されたログパスの集合。
	referenced := map[string]bool{}
	if err := env.deps.Store.View(ctx, func(state *coreserver.State) error {
		for _, rs := range state.Repos {
			for _, runtime := range rs.Servers {
				if runtime == nil {
					continue
				}
				if runtime.Running != nil && runtime.Running.Log != "" {
					referenced[runtime.Running.Log] = true
				}
				if runtime.LastLog != "" {
					referenced[runtime.LastLog] = true
				}
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}

	var out []Finding
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		repoName, serverName, worktree, _, ok := coreserver.ParseLogName(entry.Name())
		if !ok {
			continue
		}
		path := filepath.Join(logsDir, entry.Name())
		// .prev は現行ログの参照に随伴する（現行が参照されていれば前世代も残す）。
		if referenced[strings.TrimSuffix(path, ".prev")] {
			continue
		}
		if env.worktreeExists(worktree) {
			continue
		}
		out = append(out, Finding{
			Repo: repoName, Server: serverName, Worktree: worktree, Path: path, Fixable: true,
		})
	}
	return out, nil
}

func (orphanLogs) Fix(_ context.Context, _ *checkEnv, f Finding) error {
	if err := os.Remove(f.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// ----- チェック 5: git worktree prune -----

// pruneWorktrees は各リポジトリで prune 対象の古い worktree メタデータ（rm -rf
// された作業ツリーの残骸）を dry-run で検出し、--fix で `git worktree prune` を
// 実行する。
type pruneWorktrees struct{}

func (pruneWorktrees) Name() string { return "prunable_worktrees" }

func (pruneWorktrees) Scan(ctx context.Context, env *checkEnv) ([]Finding, error) {
	var out []Finding
	for _, r := range env.repos {
		report, err := git.PruneWorktreesDryRun(ctx, r.Path)
		if err != nil {
			// 検査自体の失敗（名ばかり .git など）は報告のみの発見として残す。
			// リポジトリの健全性そのものは brokenRepos が別途検査する。
			out = append(out, Finding{Repo: r.Name, Path: r.Path, Detail: err.Error()})
			continue
		}
		if strings.TrimSpace(report) == "" {
			continue
		}
		out = append(out, Finding{Repo: r.Name, Path: r.Path, Detail: report, Fixable: true})
	}
	return out, nil
}

func (pruneWorktrees) Fix(ctx context.Context, env *checkEnv, f Finding) error {
	return env.deps.Root.WithRepoLock(ctx, f.Repo, func() error {
		return git.PruneWorktrees(ctx, f.Path)
	})
}

// ----- チェック 6: repos_dir の実体検証（報告のみ） -----

// brokenRepos は repos_dir の各エントリの .git 実体を検証し、「名ばかり .git」
// （探索には引っかかるが git が管理していないディレクトリ）を報告する。修復は
// ユーザーの判断（clone し直す・除去する）に委ねる。
type brokenRepos struct{}

func (brokenRepos) Name() string { return "broken_repo" }

func (brokenRepos) Scan(ctx context.Context, env *checkEnv) ([]Finding, error) {
	var out []Finding
	for _, r := range env.repos {
		ok, err := git.HasGitDir(ctx, r.Path)
		if err != nil {
			return nil, err
		}
		if !ok {
			out = append(out, Finding{Repo: r.Name, Path: r.Path})
		}
	}
	return out, nil
}

func (brokenRepos) Fix(context.Context, *checkEnv, Finding) error {
	return errors.New("報告のみのチェックです")
}

// ----- チェック 7: 設定と実体の突き合わせ（報告のみ） -----

// configWithoutRepo は設定の [repos.<name>] に居るが repos_dir に実在しない
// リポジトリを報告する（タイプミス・移動済みの検出）。
type configWithoutRepo struct{}

func (configWithoutRepo) Name() string { return "config_without_repo" }

func (configWithoutRepo) Scan(_ context.Context, env *checkEnv) ([]Finding, error) {
	have := map[string]bool{}
	for _, r := range env.repos {
		have[r.Name] = true
	}
	var out []Finding
	for _, name := range slices.Sorted(maps.Keys(env.deps.Config.Repos)) {
		if !have[name] {
			out = append(out, Finding{Repo: name})
		}
	}
	return out, nil
}

func (configWithoutRepo) Fix(context.Context, *checkEnv, Finding) error {
	return errors.New("報告のみのチェックです")
}

// repoWithoutServers は repos_dir に実在するがサーバー設定（[repos.<name>.servers]）を
// 持たないリポジトリを報告する（server switch の対象にならないことの可視化）。
type repoWithoutServers struct{}

func (repoWithoutServers) Name() string { return "repo_without_servers" }

func (repoWithoutServers) Scan(_ context.Context, env *checkEnv) ([]Finding, error) {
	servers := env.deps.Config.ServersConfig()
	var out []Finding
	for _, r := range env.repos {
		if _, ok := servers[r.Name]; ok {
			continue
		}
		out = append(out, Finding{Repo: r.Name, Path: r.Path})
	}
	return out, nil
}

func (repoWithoutServers) Fix(context.Context, *checkEnv, Finding) error {
	return errors.New("報告のみのチェックです")
}
