package hooks

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/vimrak-hal/worktree-integrator/internal/core/wtenv"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/childio"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/parallel"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/proc"
)

// job は作業の単位。1 つのフックと、任意でひも付けられたリポジトリを保持する。
type job struct {
	hook *Hook
	repo *wtenv.RepoContext
}

// Run は（単一のタイミングの）hooks を並列に実行し、フックが宣言された順に
// その結果を返す。ctx のキャンセルは実行中のフックを終了させ、その結果は失敗
// （allow_failure なら警告）として報告される。
func Run(ctx context.Context, hooks []Hook, run *wtenv.RunContext, cio childio.Streams) []Outcome {
	jobs := make([]job, len(hooks))
	for i := range hooks {
		jobs[i] = job{hook: &hooks[i]}
	}
	return runJobs(ctx, jobs, run, cio)
}

// RunWorktree は repos 内の各リポジトリについて hooks を一度ずつ、すべて並列に実行し、
// それぞれをそのリポジトリのコンテキストにひも付ける。結果はリポジトリごとに
// （repos の順で）グループ化され、各グループ内ではフックの宣言順に並ぶ。
func RunWorktree(ctx context.Context, hooks []Hook, run *wtenv.RunContext, repos []wtenv.RepoContext, cio childio.Streams) []Outcome {
	var jobs []job
	for ri := range repos {
		for hi := range hooks {
			jobs = append(jobs, job{hook: &hooks[hi], repo: &repos[ri]})
		}
	}
	return runJobs(ctx, jobs, run, cio)
}

// runJobs はすべての job を有界の並列度で実行し、結果を job の順に収集する。
// フックはリポジトリ数 × フック本数だけの子プロセスを起こしうるため、無制限に
// 一斉起動せず parallel.AutoLimit()（job 数との min）を上限として抑える。これは
// worktree 側の並列作成と一貫した有界化である。taggedWriter による出力の混線防止と
// Outcome の順序は、この有界化の影響を受けない。
func runJobs(ctx context.Context, jobs []job, run *wtenv.RunContext, cio childio.Streams) []Outcome {
	if len(jobs) == 0 {
		return nil
	}
	limit := min(parallel.AutoLimit(), len(jobs))
	return parallel.Map(ctx, limit, jobs, func(ctx context.Context, _ int, j job) Outcome {
		return execute(ctx, j.hook, run, j.repo, cio)
	})
}

// execute は単一のフックを完了まで実行し、その結果を返す。コマンドは WT_* 環境変数を
// 設定した状態で `sh -c <command>` として呼び出される。出力は hook 名でタグ付けされ、
// 並列実行される他のフックの出力と行の途中で混ざらないようにする。
func execute(ctx context.Context, hook *Hook, run *wtenv.RunContext, repo *wtenv.RepoContext, cio childio.Streams) Outcome {
	// 作業ディレクトリ: 明示的な指定があればそれを優先する。なければ after_worktree
	// フックはその worktree 内で実行され、それ以外はカレントディレクトリを引き継ぐ。
	var dir string
	switch {
	case hook.Workdir != "":
		dir = hook.Workdir
	case repo != nil:
		dir = repo.WorktreePath
	}
	env := wtenv.EnvPairs(run, repo)

	// timeout_secs が設定されていれば、このフック専用の期限付き ctx を作る（省略時
	// 0 は無制限で、親 ctx をそのまま使う）。
	hookCtx := ctx
	if hook.TimeoutSecs > 0 {
		var cancel context.CancelFunc
		hookCtx, cancel = context.WithTimeout(ctx, time.Duration(hook.TimeoutSecs)*time.Second)
		defer cancel()
	}

	streams, closeStreams := taggedStreams(cio, hook.Name)
	err := proc.Run(hookCtx, hook.Command.Script(), dir, wtenv.Environ(os.Environ(), env), streams)
	closeStreams()

	if err == nil {
		return succeeded(hook.Name)
	}
	// キャンセルで殺されたフックも "signal: killed" を報告するため、中断・期限超過・
	// 異常終了の切り分けは proc.Classify に委ねる（親 ctx とこのフック専用の hookCtx を
	// 渡す）。結果を hooks の語彙（タイムアウト / キャンセル / 異常終了 / 起動失敗）へ写す。
	kind, _ := proc.Classify(ctx, hookCtx, err)
	switch kind {
	case proc.ResultTimedOut:
		// タイムアウトによる強制終了は、単なるキャンセルとは別の理由として報告する。
		return failed(hook.Name,
			fmt.Sprintf("タイムアウト（%d 秒）のため強制終了しました", hook.TimeoutSecs), hook.AllowFailure)
	case proc.ResultCanceled:
		// 何が起きたかが分かるよう、ctx 起因の失敗はキャンセルとして明示する。
		return failed(hook.Name, "canceled: "+ctx.Err().Error(), hook.AllowFailure)
	case proc.ResultExitNonZero:
		// 終了状況（"exit status N" / "signal: ..."）をそのまま見せるため、元の
		// *exec.ExitError を取り出して整形する。
		var exit *exec.ExitError
		_ = errors.As(err, &exit)
		return failed(hook.Name, fmt.Sprintf("exited unsuccessfully (%s)", exit), hook.AllowFailure)
	default: // ResultStartFailed
		return failed(hook.Name, "failed to run: "+err.Error(), hook.AllowFailure)
	}
}

// taggedStreams は cio の Stdout/Stderr を、hook 名でタグ付けした行単位の出力に
// 差し替える。同じタイミングのフックはすべて並列実行されるため、生のまま素通しすると
// 複数フックの出力が行の途中で混ざりうる。各ストリームを io.Pipe 越しに 1 行ずつ
// 中継し、"[name] " を前置してから元の書き込み先へ流すことでこれを防ぐ。返す
// closeStreams は、フックの実行が終わった直後に呼ぶこと（パイプを閉じ、中継用の
// goroutine の完了を待つ）。
func taggedStreams(cio childio.Streams, name string) (childio.Streams, func()) {
	stdout, closeOut := taggedWriter(cio.Stdout, name)
	stderr, closeErr := taggedWriter(cio.Stderr, name)
	return childio.Streams{Stdin: cio.Stdin, Stdout: stdout, Stderr: stderr}, func() {
		closeOut()
		closeErr()
	}
}

// taggedWriter は "[name] " を前置した行を dst へ書き出す io.Writer を返す。dst が
// nil（送り先なし）なら nil をそのまま返す。返す close は、書き込みが終わった後に
// 呼んでパイプを閉じ、中継用 goroutine の完了を待つこと。
func taggedWriter(dst io.Writer, name string) (io.Writer, func()) {
	if dst == nil {
		return nil, func() {}
	}
	r, w := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(r)
		// 既定の 64KiB を超える行（稀だが起こりうる）でも黙って打ち切らないよう、
		// 上限を広げておく。
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			fmt.Fprintf(dst, "[%s] %s\n", name, scanner.Text())
		}
	}()
	return w, func() {
		_ = w.Close()
		<-done
	}
}
