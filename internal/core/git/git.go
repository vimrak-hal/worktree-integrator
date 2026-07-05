// Package git は「git とのやり取り」を一手に担う。ワークツリーワークフローで
// 使われるローカルの `git` コマンドへの薄いラッパーと、`.git` エントリによって
// ワーキングツリーを判定する小さなファイルシステム上の述語から成る。
//
// 各コマンド関数は `git` を外部実行するため、実行時の要件は PATH 上の `git`
// だけであり、ネイティブの git ライブラリはリンクしていない。すべてのコマンド関数は
// context.Context を第 1 引数に取り、キャンセル（Ctrl-C / MCP クライアントの中断）で
// 実行中の git プロセスを終了させる。
//
// 認証はローカルの git の設定に従う。ssh-agent（SSH_AUTH_SOCK を尊重）、
// クレデンシャルヘルパーなどである。対話的なクレデンシャル入力は無効化されており
// （GIT_TERMINAL_PROMPT=0）、クレデンシャルが無い場合はハングせず高速に失敗する。
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// waitDelay は、git 本体の終了（またはキャンセルによる kill）後も子孫プロセスが
// 標準出力/エラーのパイプを握り続けている場合に、Wait のパイプ待ちを打ち切るまでの
// 猶予。これが無いと、キャンセルで git を殺してもリモートヘルパー等の孫プロセスが
// パイプを保持し続ける限り Run が戻らず、キャンセルが「効かない」ように見える。
const waitDelay = 2 * time.Second

// IsWorkTree は dir が Git のワーキングツリーかどうか、すなわち ".git"
// エントリを含むかどうかを返す。そのエントリはディレクトリ（通常のクローン）の
// 場合もあれば、ファイル（"gitdir:" ポインタを保持するリンク済みワークツリー）の
// 場合もある。「このディレクトリは git によって管理されている」ことの唯一の
// 判断基準であり、リポジトリの探索とワークツリーの配置先の分類で共有される。
// 単一の stat のみでプロセスを起動しないため、ctx は取らない。
func IsWorkTree(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// run は `git -C dir <args...>` を実行し、トリムした標準出力を返す。失敗時は
// エラーに git の標準エラー出力を含める。ctx のキャンセルで git プロセスは
// 終了させられ、そのエラーは errors.Is(err, context.Canceled) で判別できるよう
// ctx.Err() をラップして返す。
func run(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.WaitDelay = waitDelay
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// キャンセルで殺されたプロセスは "signal: killed" を報告する。呼び出し側が
		// キャンセルを型で判別できるよう、ctx 起因の失敗は ctx.Err() を優先する。
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), ctxErr)
		}
		return "", gitError(args, stderr.String(), err)
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

// succeeds は `git -C dir <args...>` を実行し、終了コードが 0 だったかを返す。
// 非ゼロ終了は ok=false として報告され（エラーではない）、git をそもそも実行できなかった
// 場合とキャンセルされた場合のみエラーとなる。
func succeeds(ctx context.Context, dir string, args ...string) (bool, error) {
	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.WaitDelay = waitDelay
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, fmt.Errorf("git %s: %w", strings.Join(args, " "), ctxErr)
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return false, nil
		}
		return false, gitError(args, "", err)
	}
	return true, nil
}

func gitError(args []string, stderr string, err error) error {
	stderr = strings.TrimSpace(stderr)
	if stderr != "" {
		return fmt.Errorf("git %s: %s", strings.Join(args, " "), stderr)
	}
	return fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
}

// FetchRef は dir のリポジトリで、remote から branch 1 本だけを、タグをスキップして
// フェッチする。クローン時の既定の refspec（`+refs/heads/*:refs/remotes/<remote>/*`）が
// 効いている限り、リモート追跡 ref（refs/remotes/<remote>/<branch>）もこれで更新される。
// 1 本だけを指定することで、無関係な他ブランチの更新を避けて高速化する（旧実装は
// remote 全体を fetch していた）。
func FetchRef(ctx context.Context, dir, remote, branch string) error {
	_, err := run(ctx, dir, "fetch", "--no-tags", remote, branch)
	return err
}

// runOptional は `git -C dir <args...>` を実行し、(標準出力, ok, err) を返す。
// クリーンな非ゼロ終了（「見つからなかった」の意）は ok=false・err=nil として区別する。
// err が非 nil になるのは、コマンド自体を実行できなかった場合とキャンセルされた
// 場合のみである。
func runOptional(ctx context.Context, dir string, args ...string) (string, bool, error) {
	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.WaitDelay = waitDelay
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", false, fmt.Errorf("git %s: %w", strings.Join(args, " "), ctxErr)
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", false, nil
		}
		return "", false, gitError(args, "", err)
	}
	return strings.TrimRight(stdout.String(), "\n"), true, nil
}

// DefaultBranch は remote のデフォルトブランチ名を解決する。解決順序は
// `git symbolic-ref refs/remotes/<remote>/HEAD`（クローン時に設定される、リモートが
// 報告した既定ブランチ）→ main の存在確認 → master の存在確認。いずれも見つからない
// 場合はエラーになる。ネットワークにはアクセスしない（ローカルの ref を見るのみ）。
func DefaultBranch(ctx context.Context, dir, remote string) (string, error) {
	prefix := fmt.Sprintf("refs/remotes/%s/", remote)
	ref, ok, err := runOptional(ctx, dir, "symbolic-ref", "--quiet", prefix+"HEAD")
	if err != nil {
		return "", err
	}
	if ok {
		if branch, found := strings.CutPrefix(ref, prefix); found && branch != "" {
			return branch, nil
		}
	}
	for _, branch := range []string{"main", "master"} {
		exists, err := RemoteBranchExists(ctx, dir, remote, branch)
		if err != nil {
			return "", err
		}
		if exists {
			return branch, nil
		}
	}
	return "", fmt.Errorf("リモート %s のデフォルトブランチを解決できません（HEAD の symbolic-ref も main/master も見つかりません）", remote)
}

// ResolveTip は remote/branch の最新コミットのハッシュを解決する。
func ResolveTip(ctx context.Context, dir, remote, branch string) (string, error) {
	ref := fmt.Sprintf("refs/remotes/%s/%s^{commit}", remote, branch)
	out, ok, err := runOptional(ctx, dir, "rev-parse", "--verify", "-q", ref)
	if err != nil {
		return "", err
	}
	if !ok || out == "" {
		return "", fmt.Errorf("リモートブランチ %s/%s が見つかりません", remote, branch)
	}
	return out, nil
}

// LocalBranchExists は dir のリポジトリに refs/heads/<branch> が存在するかを返す。
func LocalBranchExists(ctx context.Context, dir, branch string) (bool, error) {
	return succeeds(ctx, dir, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
}

// RemoteBranchExists は dir のリポジトリに refs/remotes/<remote>/<branch> が
// 存在するかを返す。fetch 失敗時のオフライン degrade（既存の追跡ブランチから作成を
// 続行できるか）の判定にも使われる。
func RemoteBranchExists(ctx context.Context, dir, remote, branch string) (bool, error) {
	return succeeds(ctx, dir, "show-ref", "--verify", "--quiet", "refs/remotes/"+remote+"/"+branch)
}

// PruneWorktrees は作業ツリーが失われた古い連結ワークツリーのメタデータ
// （例: `git worktree remove` ではなく `rm -rf` で削除されたディレクトリ）を
// 削除する。これにより、古い管理情報と衝突せずに名前を再作成できる。
// 生きているワークツリーには一切手を触れない。
func PruneWorktrees(ctx context.Context, dir string) error {
	_, err := run(ctx, dir, "worktree", "prune")
	return err
}

// PruneWorktreesDryRun は PruneWorktrees が削除する対象の報告（`git worktree prune
// --dry-run --verbose` の出力）を、何も削除せずに返す。空文字列は「掃除すべき
// メタデータが無い」を意味する。doctor が --fix なしで発見を報告するために使う。
//
// git はこの dry-run 報告を（stdout ではなく）stderr に書くため、共通の run では
// なく stderr を戻り値として取り込む専用の実行を行う。
func PruneWorktreesDryRun(ctx context.Context, dir string) (string, error) {
	args := []string{"worktree", "prune", "--dry-run", "--verbose"}
	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.WaitDelay = waitDelay
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), ctxErr)
		}
		return "", gitError(args, stderr.String(), err)
	}
	return strings.TrimRight(stderr.String(), "\n"), nil
}

// RemoveWorktree は dir のリポジトリから target の連結ワークツリーを削除する
// （作業ディレクトリと管理メタデータの両方）。未コミットの変更が残っている場合、
// git は削除を拒否してエラーになる（意図的な安全弁）。force はその拒否を
// `--force` で上書きする。ブランチは削除しない（DeleteBranch が別途担う）。
func RemoveWorktree(ctx context.Context, dir, target string, force bool) error {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, target)
	_, err := run(ctx, dir, args...)
	return err
}

// DeleteBranch は dir のリポジトリから refs/heads/<branch> を強制削除する
// （`git branch -D`。マージ済みかは問わない — worktree ライフサイクルの後始末は
// 「作った名前を消す」ことが目的で、残すかどうかの判断はユーザーが --keep-branch で
// 行う）。ブランチがどこかのワークツリーでチェックアウトされたままなら git が
// 拒否してエラーになる。
func DeleteBranch(ctx context.Context, dir, branch string) error {
	_, err := run(ctx, dir, "branch", "-D", branch)
	return err
}

// CurrentBranch は dir でチェックアウトされているブランチ名を返す（detached HEAD
// では "HEAD"）。gitdir ポインタが壊れている（rm -rf 残骸などで実体を失った）
// ワークツリーではエラーになる — inventory はそれを Healthy=false として扱う。
func CurrentBranch(ctx context.Context, dir string) (string, error) {
	return run(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
}

// HasGitDir は dir の ".git" エントリが実際に有効な git ディレクトリ（または有効な
// gitdir ポインタ）かを返す。".git" というエントリが存在するだけの「名ばかり
// リポジトリ」の検出（doctor）に使う。`git rev-parse --git-dir` ではなく
// `--resolve-git-dir` を使うのは、前者が無効な .git を黙ってスキップして上位
// ディレクトリのリポジトリを報告してしまう（偽陰性）のに対し、後者は与えた
// エントリ自体を検証するためである。判定できなかった場合（git を実行できない・
// キャンセル）のみエラーを返す。
func HasGitDir(ctx context.Context, dir string) (bool, error) {
	return succeeds(ctx, dir, "rev-parse", "--resolve-git-dir", filepath.Join(dir, ".git"))
}

// AddWorktree は startPoint に branch という名前の新しいブランチを作成し、それを
// チェックアウトした連結ワークツリーを target に追加する。ブランチは（スラッシュを
// 含みうる）完全なワークツリー名を保持する。git はワークツリーのメタデータ ID を
// target のベース名自体から導出する。
func AddWorktree(ctx context.Context, dir, branch, target, startPoint string) error {
	_, err := run(ctx, dir, "worktree", "add", "-b", branch, target, startPoint)
	return err
}

// IgnoredPaths はリポジトリの gitignore されたエントリを、リポジトリルートからの
// 相対パスで列挙する。完全に無視されるディレクトリは単一のエントリにまとめられる
// （例: その配下の全ファイルではなく "node_modules"）。呼び出し側はまとめられた
// ディレクトリをコピーする際、除外指定を再帰的に適用する。
func IgnoredPaths(ctx context.Context, dir string) ([]string, error) {
	// -z: パスを正確に保持するため、NUL 区切りでクオートなしの出力にする。
	out, err := run(ctx, dir, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "-z")
	if err != nil {
		return nil, err
	}
	var paths []string
	for p := range strings.SplitSeq(out, "\x00") {
		if p == "" {
			continue
		}
		// まとめられたディレクトリは末尾スラッシュ付きで報告される。コピー処理が
		// 通常のディレクトリパスとして扱えるよう、それを取り除く。
		paths = append(paths, strings.TrimSuffix(p, "/"))
	}
	return paths, nil
}
