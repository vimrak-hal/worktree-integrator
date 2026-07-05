package server

import (
	"context"

	"github.com/vimrak-hal/worktree-integrator/internal/core/action"
	coreserver "github.com/vimrak-hal/worktree-integrator/internal/core/server"
)

// Stop は対象サーバーを停止し、型付きの結果を返す。1 つ以上の停止に失敗した場合、
// （そこまでの結果を保持した）Result とともに非 nil のエラーを返す。
func Stop(ctx context.Context, d Deps, cmd action.ServerCommand, k action.StopKind) (*StopResult, error) {
	targets, unknown, err := resolveUnderStateLock(ctx, d.Store, cmd)
	if err != nil {
		return nil, err
	}
	res := &StopResult{UnknownRepos: unknown}

	for _, tg := range targets {
		if err := d.Root.WithRepoLock(ctx, tg.Repo, func() error {
			return stopRepo(ctx, d, res, k, tg)
		}); err != nil {
			return res, err
		}
	}

	if res.Failed > 0 {
		return res, failedCountError(res.Failed, "サーバー停止")
	}
	return res, nil
}

// stopRepo は 1 つのリポジトリの対象サーバーを停止する。呼び出し元が repo 操作
// ロックを保持していることが前提。dirty（永続化の要否）は実際に記録が変わったか
// どうか（StopServer の Stopped）から導出する。停止に失敗したサーバーの Running は
// core 側の契約により保持されるため、状態が現実から乖離することはない。
func stopRepo(ctx context.Context, d Deps, res *StopResult, k action.StopKind, tg target) error {
	runtimes, err := loadRuntimes(ctx, d.Store, tg.Repo)
	if err != nil {
		return err
	}

	for _, serverName := range tg.Names {
		runtime := runtimes[serverName]
		if runtime == nil {
			continue
		}
		// 任意の worktree 名フィルタはサーバー単位: 「その worktree で動いている
		// サーバーを止める」（Instance.Worktree の照合）。リポジトリ単位のアクティブ
		// 概念は存在しない。
		if one, ok := k.Scope.(action.OneWorktree); ok {
			if runtime.Running == nil || runtime.Running.Worktree != one.Name.String() {
				continue
			}
		}
		sres := coreserver.StopServer(ctx, d.Proc, runtime)
		outcome := ServerOutcome{Repo: tg.Repo, Server: serverName}
		for _, ev := range sres.Events {
			d.emit(tg.Repo, serverName, ev)
			outcome.Events = append(outcome.Events, eventDTO(ev))
		}
		switch {
		case sres.Stopped:
			outcome.Status = OutcomeStopped
			res.Stopped++
			// dirty は実変更から導出: 記録が実際にクリアされた場合のみ書き戻す。
			if err := saveRuntime(ctx, d.Store, tg.Repo, serverName, runtime); err != nil {
				return err
			}
		case sres.Failed:
			outcome.Status = OutcomeStopFailed
			res.Failed++
		default:
			// 稼働記録が無く、何も起きなかった。結果には現れない。
			continue
		}
		res.PerServer = append(res.PerServer, outcome)
	}
	return nil
}
