package tree

import (
	"context"
	"maps"
	"slices"

	"github.com/vimrak-hal/worktree-integrator/internal/core/inventory"
	coreserver "github.com/vimrak-hal/worktree-integrator/internal/core/server"
)

// ListResult は `list` の結果。inventory（実体スキャン）・別名・サーバー状態の
// 統合ビューである。
type ListResult struct {
	// WorktreesDir はスキャンしたベースディレクトリ。
	WorktreesDir string `json:"worktrees_dir"`
	// Worktrees は発見された worktree セット（名前順）。
	Worktrees []WorktreeRow `json:"worktrees"`
	// LegacyBackup は旧形式の状態ファイルを退避した先のパス（発生時のみ）。
	LegacyBackup string `json:"legacy_state_backup,omitempty"`
}

// SetLegacyBackup は旧形式状態ファイルの退避先を記録する（App が退避の有無を写す）。
func (r *ListResult) SetLegacyBackup(bak string) { r.LegacyBackup = bak }

// WorktreeRow は list テーブルの 1 行。
type WorktreeRow struct {
	// Name は worktree 名（worktrees_dir からの相対パス）。
	Name string `json:"name"`
	// Root は worktree のルートディレクトリ。
	Root string `json:"root"`
	// Alias は表示用別名（無ければ空）。
	Alias string `json:"alias,omitempty"`
	// Broken は壊れたチェックアウト（gitdir ポインタが死んでいる）を含むかどうか。
	// 表示層が (!) マークと doctor --fix の案内を付ける。
	Broken bool `json:"broken,omitempty"`
	// Repos はこの worktree に入っているリポジトリのチェックアウト。
	Repos []RepoCell `json:"repos,omitempty"`
	// Servers はこの worktree で現在稼働中のサーバー。
	Servers []ServerCell `json:"servers,omitempty"`
}

// RepoCell は 1 つのチェックアウトの表示情報。
type RepoCell struct {
	Repo    string `json:"repo"`
	Branch  string `json:"branch,omitempty"`
	Healthy bool   `json:"healthy"`
}

// ServerCell は稼働中のサーバー 1 つの表示情報。
type ServerCell struct {
	Repo   string `json:"repo"`
	Server string `json:"server"`
	Pid    int    `json:"pid"`
}

// List は worktrees_dir をスキャンし、別名とサーバー状態を重ねた統合ビューを返す。
// server status と同じく、消滅済みプロセスの稼働記録は Probe が自己修復する
// （変更があれば永続化される）。
func List(ctx context.Context, d Deps) (*ListResult, error) {
	scanned, err := inventory.Scan(ctx, d.WorktreesDir)
	if err != nil {
		return nil, err
	}

	// 別名の読み取り失敗は致命的ではなく、ALIAS 列が空に低下するだけ（status と
	// 同じ扱い）。状態ロックの前に読み、ロックの入れ子を避ける。
	aliasMap := map[string]string{}
	if a, err := d.Aliases.Load(ctx); err == nil {
		aliasMap = a.Aliases
	}

	// worktree 名 → 稼働中サーバー。走査は決定的な順序（repo 名 → server 名）。
	running := map[string][]ServerCell{}
	err = d.Store.Update(ctx, func(state *coreserver.State) (bool, error) {
		changed := false
		for _, repoName := range slices.Sorted(maps.Keys(state.Repos)) {
			rs := state.Repos[repoName]
			for _, serverName := range slices.Sorted(maps.Keys(rs.Servers)) {
				runtime := rs.Servers[serverName]
				if runtime == nil || runtime.Running == nil {
					continue
				}
				worktree := runtime.Running.Worktree
				st, pid, modified := coreserver.Probe(ctx, d.Proc, runtime)
				changed = changed || modified
				if st == coreserver.StatusRunning {
					running[worktree] = append(running[worktree], ServerCell{
						Repo: repoName, Server: serverName, Pid: pid,
					})
				}
			}
		}
		return changed, nil
	})
	if err != nil {
		return nil, err
	}

	// Worktrees は空でも非 nil（JSON / structuredContent で null にならない）。
	res := &ListResult{WorktreesDir: d.WorktreesDir, Worktrees: []WorktreeRow{}}
	for _, wt := range scanned {
		row := WorktreeRow{
			Name:   wt.Name,
			Root:   wt.Root,
			Alias:  aliasMap[wt.Name],
			Broken: wt.Broken(),
		}
		for _, r := range wt.Repos {
			row.Repos = append(row.Repos, RepoCell{Repo: r.Repo, Branch: r.Branch, Healthy: r.Healthy})
		}
		row.Servers = running[wt.Name]
		res.Worktrees = append(res.Worktrees, row)
	}
	return res, nil
}
