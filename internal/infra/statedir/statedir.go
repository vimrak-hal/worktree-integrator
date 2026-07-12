// Package statedir は、ツールの永続状態が置かれるルートディレクトリの単一の解決点で
// ある。状態を構成する各ファイル・ディレクトリ（servers.toml / aliases.toml / logs/ /
// locks/）のパスはすべて Root から導出され、XDG_STATE_HOME の解決はこのパッケージの
// Default にのみ存在する。
//
// これまでは alias.DefaultDir / server.DefaultStateDir / store.DefaultDir という
// 三重の転送によって「両ストアが同じディレクトリを共有する」ことが暗黙に成立していた。
// 呼び出し側が Root を一度解決して各ストアへ注入することで、その共有を明示的な
// 契約にする。
//
// Root はリポジトリ単位の操作ロック（WithRepoLock）も提供する。これは switch / stop の
// ようなワークフロー全体を同一リポジトリについて直列化するためのプロセス跨ぎのロックで、
// 状態ファイル自体の短命ロック（infra/store が担う）とは役割が異なる。ロック順序は
// 「repo 操作ロック → 状態ファイルロック」であり、逆順に取得してはならない。
// 本格的な利用はサーバー状態機械の再設計（Phase 3）が担う。
package statedir

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/vimrak-hal/worktree-integrator/internal/infra/store"
)

// ErrBusy は、リポジトリ操作ロックをタイムアウトまでに取得できなかったことを表す。
// 状態ファイルロック（infra/store）の ErrBusy と同一の値であり、呼び出し側は
// errors.Is 一つで「別のコマンドが実行中」（どちらのロック段でも）を検出できる。
var ErrBusy = store.ErrBusy

// repoLockTimeout はリポジトリ操作ロックの取得を諦めるまでの待ち時間。
const repoLockTimeout = 5 * time.Second

// Root は状態ディレクトリのルート。Default または At でのみ構築される。
type Root struct {
	dir string
}

// Default は既定の状態ルートを解決する。$XDG_STATE_HOME/worktree-integrator、
// XDG_STATE_HOME が未設定の場合は ~/.local/state/worktree-integrator となる。
// XDG_STATE_HOME の解決はコードベース全体でここ 1 箇所のみである。
func Default() (Root, error) {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return At(filepath.Join(xdg, "worktree-integrator")), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Root{}, fmt.Errorf("could not determine the home directory: %w", err)
	}
	return At(filepath.Join(home, ".local", "state", "worktree-integrator")), nil
}

// At は dir をルートとする Root を返す。テストが一時ディレクトリを状態ルートとして
// 注入するために使う。
func At(dir string) Root { return Root{dir: dir} }

// Dir は状態ルートのディレクトリパス。
func (r Root) Dir() string { return r.dir }

// ServersFile はサーバー状態ドキュメント（servers.toml）のパス。
func (r Root) ServersFile() string { return filepath.Join(r.dir, "servers.toml") }

// AliasesFile は worktree 別名ドキュメント（aliases.toml）のパス。
func (r Root) AliasesFile() string { return filepath.Join(r.dir, "aliases.toml") }

// LogsDir はサーバーごとのログファイルを保持するディレクトリのパス。
func (r Root) LogsDir() string { return filepath.Join(r.dir, "logs") }

// RepoLockPath は repo のリポジトリ操作ロックファイル（locks/<repo>.lock）のパス。
func (r Root) RepoLockPath(repo string) string {
	return filepath.Join(r.dir, "locks", repo+".lock")
}

// WithRepoLock は repo の操作ロック（flock）を保持した状態で fn を実行する。
// ロックはプロセス跨ぎ（CLI と MCP の同時実行）でも同一リポジトリへの操作を
// 直列化する。取得はノンブロッキング + 小刻みな再試行で行い、ctx のキャンセルには
// ctx.Err() で、タイムアウト（既定 5 秒）には ErrBusy で応答する。
func (r Root) WithRepoLock(ctx context.Context, repo string, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path := r.RepoLockPath(repo)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create lock directory %s: %w", filepath.Dir(path), err)
	}
	// ロック取得の再試行意味論（LOCK_NB + 小刻みな再試行 → ctx キャンセル →
	// タイムアウトで ErrBusy）は状態ファイルロックと同一であり、infra/store の共通
	// ヘルパーへ委譲する。ロック対象は専用のロックファイルであり、内容の書き込みも
	// truncate も行わないため、flock の inode への作用でロックの同一性が保たれる。
	file, err := store.AcquireLock(ctx, path, syscall.LOCK_EX, repoLockTimeout)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	defer func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN) }()

	return fn()
}
