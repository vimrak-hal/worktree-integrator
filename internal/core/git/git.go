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
	// 多層防御: remote・branch は位置引数として git fetch に渡る。上位（action の
	// 検証）を通り抜けても、'-' で始まる値は git のオプション（"--upload-pack=<cmd>"
	// による任意コマンド実行など）へ化けるため、コマンド実行前にここで弾く。
	if strings.HasPrefix(remote, "-") {
		return fmt.Errorf("リモート %q が不正です: '-' で始まる名前は git のオプションとして解釈されるため使えません", remote)
	}
	if strings.HasPrefix(branch, "-") {
		return fmt.Errorf("ブランチ %q が不正です: '-' で始まる名前は git のオプションとして解釈されるため使えません", branch)
	}
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
