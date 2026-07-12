package server

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/vimrak-hal/worktree-integrator/internal/app/action"
	coreserver "github.com/vimrak-hal/worktree-integrator/internal/core/server"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/wtenv"
)

// Switch は対象リポジトリの全サーバーを k.Name の worktree へ切り替え、型付きの
// 結果を返す。1 つ以上のサーバー操作が失敗した場合、（そこまでの結果を保持した）
// Result とともに非 nil のエラーを返す。
func Switch(ctx context.Context, d Deps, cmd action.ServerCommand, k action.SwitchKind) (*SwitchResult, error) {
	targets, unknown, err := resolveUnderStateLock(ctx, d.Store, cmd)
	if err != nil {
		return nil, err
	}
	res := &SwitchResult{UnknownRepos: unknown}
	if len(targets) == 0 {
		res.NoServerConfig = cmd.Servers.IsEmpty()
		return res, nil
	}

	run := wtenv.NewRunContext(k.Name.String(), cmd.ReposDir, cmd.WorktreesDir)

	for _, tg := range targets {
		// 設定の無い（状態にだけ稼働記録が残る）リポジトリは切り替えられない
		//（start コマンドが無い）。記録には触れず、スキップとして報告する
		//（server stop で停止できる）。
		if len(tg.Specs) == 0 {
			for _, name := range tg.Names {
				res.PerServer = append(res.PerServer, ServerOutcome{
					Repo: tg.Repo, Server: name, Status: OutcomeSkipped, Reason: ReasonNoServerConfig,
				})
			}
			continue
		}
		repoRoot := filepath.Join(run.Root, tg.Repo)
		if !pathExists(repoRoot) {
			if k.RequireWorktree {
				res.tally()
				return res, fmt.Errorf("worktree が見つかりません: %s", repoRoot)
			}
			for _, name := range tg.Specs.SortedServerNames() {
				res.PerServer = append(res.PerServer, ServerOutcome{
					Repo: tg.Repo, Server: name, Status: OutcomeSkipped,
					Reason: ReasonMissingWorktree, Path: repoRoot,
				})
			}
			continue
		}

		// リポジトリ操作ロックがこのリポジトリのワークフロー全体（状態読み →
		// プロセス操作 → 状態書き）をプロセス跨ぎで直列化する。状態ファイルロックは
		// 各ステップ内の短命ロックのみで、プロセス操作中は保持しない（他リポジトリの
		// status / logs をブロックしない）。
		if err := d.Root.WithRepoLock(ctx, tg.Repo, func() error {
			return switchRepo(ctx, d, res, cmd, k, run, tg, repoRoot)
		}); err != nil {
			res.tally()
			return res, err
		}
	}

	res.tally()
	if res.Failed > 0 {
		return res, failedCountError(res.Failed, "サーバー操作")
	}
	return res, nil
}

// switchRepo は 1 つのリポジトリの全サーバーを対象ワークツリーへ切り替える。
// 呼び出し元が repo 操作ロックを保持していることが前提。サーバーごとに切り替え、
// 成否に関わらず runtime の変化（setup 記録・停止済みの旧記録・新インスタンス）を
// 逐次永続化するため、途中で失敗しても切り替え済みのサーバーは記録される。
func switchRepo(ctx context.Context, d Deps, res *SwitchResult, cmd action.ServerCommand, k action.SwitchKind, run *wtenv.RunContext, tg target, repoRoot string) error {
	repoCtx := &wtenv.RepoContext{
		RepoName:     tg.Repo,
		RepoPath:     filepath.Join(cmd.ReposDir, tg.Repo),
		WorktreePath: repoRoot,
	}
	runtimes, err := loadRuntimes(ctx, d.Store, tg.Repo)
	if err != nil {
		return err
	}

	for _, serverName := range tg.Specs.SortedServerNames() {
		spec := tg.Specs[serverName]
		runtime := runtimes[serverName]
		if runtime == nil {
			runtime = &coreserver.Runtime{}
		}
		serverCwd := repoRoot
		if spec.Dir != "" {
			serverCwd = filepath.Join(repoRoot, spec.Dir)
		}
		logPath := d.Store.LogPath(tg.Repo, serverName, k.Name.String())
		sres, err := coreserver.SwitchServer(ctx, d.Proc, runtime, &coreserver.SwitchRequest{
			Run:        run,
			Repo:       repoCtx,
			ServerName: serverName,
			Spec:       spec,
			ServerCwd:  serverCwd,
			Log:        logPath,
			Restart:    k.Restart,
		})
		outcome := ServerOutcome{Repo: tg.Repo, Server: serverName}
		for _, ev := range sres.Events {
			d.emit(tg.Repo, serverName, ev)
			outcome.Events = append(outcome.Events, eventDTO(ev))
		}
		// 失敗時も runtime は真実を保持している（停止失敗なら Running 維持、setup
		// 成功後の失敗なら setup 記録あり）ため、常に書き戻す。
		if saveErr := saveRuntime(ctx, d.Store, tg.Repo, serverName, runtime); saveErr != nil {
			return saveErr
		}
		if err != nil {
			outcome.Status = OutcomeFailed
			outcome.Failure = failureDTO(err)
		} else {
			outcome.Status = switchStatusID(sres.Status)
		}
		res.PerServer = append(res.PerServer, outcome)
	}
	return nil
}

// switchStatusID は成功した切り替えの status を JSON の語彙へ写す。封印された
// 列挙のため、未知の値はバグでありパニックさせる。
func switchStatusID(status coreserver.SwitchStatus) string {
	switch status {
	case coreserver.SwitchStarted:
		return OutcomeStarted
	case coreserver.SwitchAlreadyRunning:
		return OutcomeAlreadyRunning
	default:
		panic(fmt.Sprintf("unknown coreserver.SwitchStatus %d", status))
	}
}

// failureDTO は core/server の型付きエラーを JSON 表現へ写す。表示層はこの分類を
// キーに日本語の文言へ変換する。
func failureDTO(err error) *Failure {
	var step *coreserver.StepError
	var start *coreserver.StartError
	var stop *coreserver.StopFailedError
	switch {
	case errors.As(err, &step):
		f := &Failure{Kind: FailStep, Step: step.Step, Error: err.Error()}
		if step.Cause != nil {
			f.Error = step.Cause.Error()
		} else {
			f.Error = ""
		}
		return f
	case errors.As(err, &start):
		return &Failure{Kind: FailStart, Error: start.Cause.Error(), LogTail: start.LogTail}
	case errors.As(err, &stop):
		return &Failure{Kind: FailStop, Error: stop.Cause.Error()}
	default:
		return &Failure{Kind: FailOther, Error: err.Error()}
	}
}
