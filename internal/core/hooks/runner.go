package hooks

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/vimrak-hal/worktree-integrator/internal/core/wtenv"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/childio"
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

// runJobs はすべての job を並列に実行し（サブプロセスの処理は I/O バウンドなので
// job ごとに 1 つの goroutine を割り当てる）、結果を job の順に収集する。
func runJobs(ctx context.Context, jobs []job, run *wtenv.RunContext, cio childio.Streams) []Outcome {
	if len(jobs) == 0 {
		return nil
	}
	outcomes := make([]Outcome, len(jobs))
	var wg sync.WaitGroup
	for i := range jobs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outcomes[i] = execute(ctx, jobs[i].hook, run, jobs[i].repo, cio)
		}(i)
	}
	wg.Wait()
	return outcomes
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

	if err != nil {
		// タイムアウトによる強制終了は、単なるキャンセルとは別の理由として報告する。
		// 判定は hookCtx（このフック専用の期限）を先に見る: 親 ctx がキャンセルされて
		// いない限り、期限切れは timeout_secs の超過を意味する。
		if errors.Is(hookCtx.Err(), context.DeadlineExceeded) {
			return failed(hook.Name,
				fmt.Sprintf("タイムアウト（%d 秒）のため強制終了しました", hook.TimeoutSecs), hook.AllowFailure)
		}
		// キャンセルで殺されたフックは "signal: killed" を報告する。何が起きたかが
		// 分かるよう、ctx 起因の失敗はキャンセルとして明示する。
		if ctxErr := ctx.Err(); ctxErr != nil {
			return failed(hook.Name, "canceled: "+ctxErr.Error(), hook.AllowFailure)
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return failed(hook.Name, fmt.Sprintf("exited unsuccessfully (%s)", exit), hook.AllowFailure)
		}
		return failed(hook.Name, "failed to run: "+err.Error(), hook.AllowFailure)
	}
	return succeeded(hook.Name)
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
