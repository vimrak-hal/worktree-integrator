package server

import (
	"context"

	"github.com/vimrak-hal/worktree-integrator/internal/core/action"
	coreserver "github.com/vimrak-hal/worktree-integrator/internal/core/server"
)

// Status は（repo × server）ごとの現在の状態を型付きの結果として返す。
func Status(ctx context.Context, d Deps, cmd action.ServerCommand) (*StatusResult, error) {
	// 表示用の別名は状態ルートを共有する（App が同じ Root から両ストアを構築する）。
	// 読み取りの失敗は致命的ではなく、Alias が単に空に低下するだけである。
	// 状態ファイルのロックを取る前に読むことで、2 つのロックの入れ子を避ける。
	aliasMap := map[string]string{}
	if a, err := d.Aliases.Load(ctx); err == nil {
		aliasMap = a.Aliases
	}

	res := &StatusResult{}
	// status は読み取りが主だが、Probe がクラッシュ済みプロセスの記録を整理した場合は
	// 永続化する必要がある。store.Update が dirty 時のみ保存し、ロック解放も引き受ける。
	// repo 操作ロックは取らない（進行中の switch の途中経過が見えるのは、現実を
	// そのまま表示する status の仕様である）。
	err := d.Store.Update(ctx, func(state *coreserver.State) (bool, error) {
		targets, unknown := resolveTargets(cmd, state)
		res.UnknownRepos = unknown
		if len(targets) == 0 {
			res.NoServerConfig = cmd.Servers.IsEmpty()
			return false, nil
		}

		changed := false
		for _, tg := range targets {
			// 読み取り専用。status は状態を実体化しない。存在しない repo/server は
			// まさに「停止」である。
			repoState := state.Repos[tg.Repo]
			for _, serverName := range tg.Names {
				row := Row{Repo: tg.Repo, Server: serverName, State: StateStopped}
				if repoState != nil {
					if runtime, ok := repoState.Servers[serverName]; ok && runtime != nil {
						// Worktree は行（repo×server）ごと: 稼働インスタンスの属性
						// からの導出値であり、リポジトリ単位の Active は存在しない。
						// クラッシュ行にも「最後に動いていた worktree」を残すため、
						// Probe（自己修復でクリアし得る）の前に取り出す。
						if runtime.Running != nil {
							row.Worktree = runtime.Running.Worktree
						}
						st, pid, modified := coreserver.Probe(ctx, d.Proc, runtime)
						changed = changed || modified
						row.State = stateID(st)
						if st == coreserver.StatusRunning {
							row.Pid = pid
						}
					}
				}
				// 別名は実在の worktree 名でのみ引く（プレースホルダをキーにしない）。
				if row.Worktree != "" {
					row.Alias = aliasMap[row.Worktree]
				}
				res.Rows = append(res.Rows, row)
			}
		}
		return changed, nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}
