package tree

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/vimrak-hal/worktree-integrator/internal/app/server"
	"github.com/vimrak-hal/worktree-integrator/internal/core/action"
	"github.com/vimrak-hal/worktree-integrator/internal/core/git"
	"github.com/vimrak-hal/worktree-integrator/internal/core/inventory"
	coreserver "github.com/vimrak-hal/worktree-integrator/internal/core/server"
)

// RemoveResult は `remove` の結果。各ステップの成否を保持し、部分失敗しても
// 「どこまで進んだか」が観測できる。
type RemoveResult struct {
	// Worktree は削除対象の worktree 名。
	Worktree string `json:"worktree"`
	// Root は worktree のルートディレクトリ。
	Root string `json:"root"`
	// Stop は、この worktree で稼働していたサーバーの停止結果（ステップ 1）。
	Stop *server.StopResult `json:"stop,omitempty"`
	// Repos はメンバーごとのチェックアウト削除の結果（ステップ 2）。
	Repos []RepoRemoval `json:"repos,omitempty"`
	// SetupCleared は状態から取り除いた setup 記録の数（ステップ 3）。
	SetupCleared int `json:"setup_cleared"`
	// AliasRemoved は別名が存在して削除されたかどうか（ステップ 4）。
	AliasRemoved bool `json:"alias_removed"`
	// LogsRemoved は削除したログファイル（.prev 含む。ステップ 5）。
	LogsRemoved []string `json:"logs_removed,omitempty"`
	// RootRemoved はルートディレクトリを削除したかどうか（ステップ 6）。メンバーの
	// 削除に 1 件でも失敗した場合、残ったチェックアウトを巻き込まないよう false の
	// まま残る。
	RootRemoved bool `json:"root_removed"`
	// StateError / AliasError / LogsError / RootError は各後始末ステップの失敗
	// （発生時のみ。処理は続行される）。
	StateError string `json:"state_error,omitempty"`
	AliasError string `json:"alias_error,omitempty"`
	LogsError  string `json:"logs_error,omitempty"`
	RootError  string `json:"root_error,omitempty"`
	// LegacyBackup は旧形式の状態ファイルを退避した先のパス（発生時のみ）。
	LegacyBackup string `json:"legacy_state_backup,omitempty"`
}

// RepoRemoval は 1 つのメンバーのチェックアウト削除の結果。
type RepoRemoval struct {
	Repo string `json:"repo"`
	// Removed はチェックアウトが削除されたかどうか。
	Removed bool `json:"removed"`
	// BranchDeleted はブランチ（= worktree 名）も削除されたかどうか。
	BranchDeleted bool `json:"branch_deleted,omitempty"`
	// Error は失敗の詳細（dirty による git の拒否など。発生時のみ）。
	Error string `json:"error,omitempty"`
}

// Remove は worktree を完全な後始末付きで削除する。手順:
//
//  1. この worktree で稼働中のサーバーを停止する（repo 操作ロック → 状態ロックの
//     順序は再利用する server.Stop が守る）。停止に失敗したらここで中断する —
//     稼働中のプロセスを残したままチェックアウトやログを消してはならない。
//  2. 各メンバーのチェックアウトを `git worktree remove` で削除する。dirty な
//     チェックアウトは git が拒否する（--force で上書き）。--keep-branch 指定が
//     なければ `git branch -D <name>` でブランチも削除する。
//  3. 全サーバーの Runtime.Setup からこの worktree の記録を取り除く。
//  4. 別名を削除する。
//  5. この worktree のログ（.prev 含む）を削除する。
//  6. worktree ルートディレクトリを削除する（メンバーの削除がすべて成功した
//     場合のみ — 失敗して残ったチェックアウトを rm -rf で巻き込まない）。
//
// ステップ 2 以降の失敗は Result に集約して進められるところまで進め、1 つでも
// 失敗があれば最後に非 nil のエラーを返す。
func Remove(ctx context.Context, d Deps, act action.Remove) (*RemoveResult, error) {
	name := act.Name.String()
	root := filepath.Join(d.WorktreesDir, name)
	res := &RemoveResult{Worktree: name, Root: root}
	if !isDir(root) {
		return res, fmt.Errorf("worktree %q がありません（%s）", name, root)
	}
	members, err := inventory.Members(ctx, root)
	if err != nil {
		return res, err
	}

	// ステップ 1: サーバー停止。対象解決から停止・永続化まで server stop と同一の
	// 経路を通る（「その worktree で動いているサーバーを止める」のサーバー単位の
	// 意味論も同じ）。
	cmd := action.ServerCommand{
		ReposDir:     d.ReposDir,
		WorktreesDir: d.WorktreesDir,
		Servers:      d.Config.ServersConfig(),
	}
	stopRes, stopErr := server.Stop(ctx, d.serverDeps(), cmd, action.StopKind{Scope: action.OneWorktree{Name: act.Name}})
	res.Stop = stopRes
	if stopErr != nil {
		return res, fmt.Errorf("サーバーを停止できなかったため削除を中断します: %w", stopErr)
	}

	// ステップ 2: チェックアウトとブランチの削除。
	allRemoved := true
	for _, m := range members {
		removal := removeMember(ctx, d, name, m, act.Force, act.KeepBranch)
		if !removal.Removed {
			allRemoved = false
		}
		res.Repos = append(res.Repos, removal)
	}

	// ステップ 3: setup 記録の除去。この worktree 名の記録は全リポジトリ・全
	// サーバーから消す（同名で再作成したとき setup が再実行されるように）。
	if err := d.Store.Update(ctx, func(state *coreserver.State) (bool, error) {
		dirty := false
		for _, rs := range state.Repos {
			for _, runtime := range rs.Servers {
				if runtime == nil {
					continue
				}
				if _, ok := runtime.Setup[name]; ok {
					delete(runtime.Setup, name)
					res.SetupCleared++
					dirty = true
				}
			}
		}
		return dirty, nil
	}); err != nil {
		res.StateError = err.Error()
	}

	// ステップ 4: 別名の削除。
	if existed, err := d.Aliases.Remove(ctx, name); err != nil {
		res.AliasError = err.Error()
	} else {
		res.AliasRemoved = existed
	}

	// ステップ 5: ログの削除。ログファイル名は (repo, server, worktree) への単射
	// エンコードなので、ファイル名だけからこの worktree のログを特定できる。
	res.LogsRemoved, res.LogsError = removeLogs(d.Root.LogsDir(), name)

	// ステップ 6: ルートディレクトリの削除。ネストした名前（feature/login）の場合、
	// 空になった中間ディレクトリ（feature/）も worktrees_dir まで遡って片付ける
	// （残すと list が空の worktree として報告してしまう）。
	if allRemoved {
		if err := os.RemoveAll(root); err != nil {
			res.RootError = err.Error()
		} else {
			res.RootRemoved = true
			removeEmptyParents(root, d.WorktreesDir)
		}
	}

	return res, removeError(res)
}

// removeEmptyParents は root の親ディレクトリを worktreesDir（自身は含まない）まで
// 遡り、空であれば削除する。空でないディレクトリに達した時点で止まる（os.Remove は
// 空でないディレクトリを拒否する）。
func removeEmptyParents(root, worktreesDir string) {
	for dir := filepath.Dir(root); dir != worktreesDir && len(dir) > len(worktreesDir); dir = filepath.Dir(dir) {
		if os.Remove(dir) != nil {
			return
		}
	}
}

// removeMember は 1 つのメンバーのチェックアウトを削除し、続けてブランチを片付ける。
// repo 操作ロックの下で行い、同じリポジトリへの並行する switch / stop と直列化する。
func removeMember(ctx context.Context, d Deps, name string, m inventory.RepoEntry, force, keepBranch bool) RepoRemoval {
	out := RepoRemoval{Repo: m.Repo}
	srcRepo := filepath.Join(d.ReposDir, m.Repo)
	srcOK := git.IsWorkTree(srcRepo)

	err := d.Root.WithRepoLock(ctx, m.Repo, func() error {
		switch {
		case m.Healthy:
			// 生きているチェックアウトは git に削除させる。dirty なら git が拒否して
			// エラーが表面化する（--force で上書き）— 意図的な安全弁。
			if !srcOK {
				return fmt.Errorf("ソースリポジトリが見つかりません（%s）", srcRepo)
			}
			if err := git.RemoveWorktree(ctx, srcRepo, m.Path, force); err != nil {
				return err
			}
		default:
			// gitdir ポインタが死んでいる残骸は git が操作できないため、ディレクトリを
			// 直接消し、ソース側に残った古いメタデータを prune で片付ける（prune の
			// 失敗は doctor --fix でも回収できるため致命的にしない）。
			if err := os.RemoveAll(m.Path); err != nil {
				return err
			}
			if srcOK {
				_ = git.PruneWorktrees(ctx, srcRepo)
			}
		}
		out.Removed = true

		// ブランチの削除（worktree 名 = ブランチ名という create の契約）。既に無い
		// 場合は現実をそのまま受け入れて何もしない。
		if keepBranch || !srcOK {
			return nil
		}
		exists, err := git.LocalBranchExists(ctx, srcRepo, name)
		if err != nil || !exists {
			return err
		}
		if err := git.DeleteBranch(ctx, srcRepo, name); err != nil {
			return err
		}
		out.BranchDeleted = true
		return nil
	})
	if err != nil {
		out.Error = err.Error()
	}
	return out
}

// removeLogs は logsDir 配下からこの worktree のログ（.prev 含む）を削除する。
// このツールの命名規則に従わないファイルには触れない。
func removeLogs(logsDir, worktree string) (removed []string, errText string) {
	entries, err := os.ReadDir(logsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ""
	}
	if err != nil {
		return nil, err.Error()
	}
	var errs []error
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		_, _, wt, _, ok := coreserver.ParseLogName(entry.Name())
		if !ok || wt != worktree {
			continue
		}
		path := filepath.Join(logsDir, entry.Name())
		if err := os.Remove(path); err != nil {
			errs = append(errs, err)
			continue
		}
		removed = append(removed, path)
	}
	if err := errors.Join(errs...); err != nil {
		errText = err.Error()
	}
	return removed, errText
}

// removeError は Result に集約された各ステップの失敗を 1 つのエラーへまとめる。
// 失敗が無ければ nil。
func removeError(res *RemoveResult) error {
	var errs []error
	for _, r := range res.Repos {
		if r.Error != "" {
			errs = append(errs, fmt.Errorf("%s: %s", r.Repo, r.Error))
		}
	}
	for _, e := range []string{res.StateError, res.AliasError, res.LogsError, res.RootError} {
		if e != "" {
			errs = append(errs, errors.New(e))
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("worktree %q の削除が完了しませんでした: %w", res.Worktree, errors.Join(errs...))
}
