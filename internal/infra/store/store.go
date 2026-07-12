// Package store はロック付き・アトミック・バージョン管理された TOML ドキュメントの
// 永続化プリミティブ。
//
// サーバー状態ストアとエイリアスストアはどちらも、共有された状態ディレクトリの下に
// 小さな TOML ドキュメントを 1 つずつ、同じ規律で永続化する。すべてのアクセスは
// アドバイザリロックを保持する Session を通じて行われ、書き込みはアトミック
// （一時ファイル + リネーム）であるため、並行する呼び出しが書きかけのファイルを
// 観測することは決してない。また、存在しないファイルはドキュメントのデフォルトとして
// 読み戻される。
//
// ロックはドキュメント本体ではなく、隣接する専用のロックファイル（<file>.lock）に
// 対して flock で取得する。flock は inode に作用するため、rename でドキュメントを
// アトミックに置き換えても（inode が変わっても）ロックの同一性には影響しない。
// ロックファイル自体は作成されるだけで truncate も置き換えもされないので、この前提は
// 常に成立する。
//
// Load / Save / Lock を素のメソッドとして公開しない設計は意図的である。読み書きは
// Session（Exclusive / Shared）または Update / View を経由することが型で強制され、
// ロック無しの read-modify-write は構造的に書けない。
package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
)

// DefaultLockTimeout はロック取得を諦めるまでの既定の待ち時間。
const DefaultLockTimeout = 5 * time.Second

// lockRetryInterval は、ノンブロッキング取得の失敗後に再試行するまでの間隔。
const lockRetryInterval = 25 * time.Millisecond

// ErrBusy は、別のプロセスがロックを保持し続けたため、タイムアウトまでにロックを
// 取得できなかったことを表す。呼び出し側は errors.Is で検出できる。
var ErrBusy = errors.New("別の worktree-integrator コマンドが実行中です")

// ErrReadOnly は、読み取り専用（Shared）セッションで Save が呼ばれたことを表す。
var ErrReadOnly = errors.New("read-only (shared) session cannot save")

// Versioned は、自身のオンディスクフォーマットバージョンを報告できるドキュメント。
// これを実装するドキュメントは Load 時に検証され、ストアに宣言されたバージョン
// （New の version 引数）より新しいファイル（より新しいツールが書いたもの）は、
// 黙って読み込んで次の保存で破壊してしまう代わりにエラーとして拒否される。
type Versioned interface {
	DocVersion() uint32
}

// File は path に保存される単一の TOML ドキュメント T。アドバイザリロックの下で
// 読み書きされ、アトミックに置き換えられる。factory はデフォルト初期化された
// ドキュメントを構築するため、ディスク上に存在しないフィールドはデフォルト値を保つ。
type File[T any] struct {
	path    string
	noun    string // エラーメッセージで使われる短い名詞（例: "state"）
	factory func() *T
	// version はこのストアのドキュメントが持つ現在のフォーマットバージョン。
	// ドキュメントごとにフォーマットは独立して進化する（server 状態は v2、alias は
	// v1）ため、グローバル定数ではなくストアごとに宣言する。
	version uint32
	// lockTimeout はロック取得の待ち時間。ゼロ値は DefaultLockTimeout を意味する。
	// テストがタイムアウト経路を短時間で検証するために存在する。
	lockTimeout time.Duration
}

// New は path のドキュメントを扱うストアを返す。noun はエラーメッセージに現れ、
// version はこのビルドが書き出す（＝理解できる上限の）フォーマットバージョン、
// factory はデフォルトのドキュメントを生成する。
func New[T any](path, noun string, version uint32, factory func() *T) *File[T] {
	return &File[T]{path: path, noun: noun, version: version, factory: factory}
}

// Path はドキュメントファイルのパス。
func (f *File[T]) Path() string { return f.path }

func (f *File[T]) lockPath() string { return f.path + ".lock" }
func (f *File[T]) tmpPath() string  { return f.path + ".tmp" }

// ensureDir はドキュメントの親ディレクトリが存在しない場合に作成する。ロックファイルを
// 置くために Shared でも必要となる（状態ルート自体の作成は無害な最小限の副作用として
// 許容する。かつて View が logs/ まで作っていた種類の副作用はここには無い）。
func (f *File[T]) ensureDir() error {
	dir := filepath.Dir(f.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create state directory %s: %w", dir, err)
	}
	return nil
}

func (f *File[T]) timeout() time.Duration {
	if f.lockTimeout > 0 {
		return f.lockTimeout
	}
	return DefaultLockTimeout
}

// Session は、アドバイザリロックを保持したままドキュメントを読み書きする単位。
// Exclusive または Shared でのみ構築され、使い終えたら必ず Close する。
type Session[T any] struct {
	file     *File[T]
	lock     *os.File
	readOnly bool
}

// Exclusive は排他ロック（LOCK_EX）を取得し、読み書き可能なセッションを返す。
// 取得はノンブロッキング + 小刻みな再試行で行い、ctx のキャンセルには ctx.Err() で、
// タイムアウト（既定 5 秒）には ErrBusy で応答する。
func (f *File[T]) Exclusive(ctx context.Context) (*Session[T], error) {
	lock, err := f.acquire(ctx, syscall.LOCK_EX)
	if err != nil {
		return nil, err
	}
	return &Session[T]{file: f, lock: lock}, nil
}

// Shared は共有ロック（LOCK_SH）を取得し、読み取り専用のセッションを返す。
// 共有セッションでの Save は ErrReadOnly で失敗する。
func (f *File[T]) Shared(ctx context.Context) (*Session[T], error) {
	lock, err := f.acquire(ctx, syscall.LOCK_SH)
	if err != nil {
		return nil, err
	}
	return &Session[T]{file: f, lock: lock, readOnly: true}, nil
}

// acquire はロックファイルを開き、how（LOCK_EX / LOCK_SH）のアドバイザリロックを
// LOCK_NB + 再試行で取得する。
func (f *File[T]) acquire(ctx context.Context, how int) (*os.File, error) {
	// キャンセル済みの呼び出しには、無競合でロックが取れる場合でも即座に応答する。
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := f.ensureDir(); err != nil {
		return nil, err
	}
	return AcquireLock(ctx, f.lockPath(), how, f.timeout())
}

// AcquireLock はロックファイル path を O_CREATE で開き、how（LOCK_EX / LOCK_SH）の
// アドバイザリロックを LOCK_NB + 小刻みな再試行で取得して、ロック済みのファイルを返す。
// EWOULDBLOCK なら deadline（timeout）まで lockRetryInterval 間隔で再試行し、ctx の
// キャンセルには ctx.Err() で、タイムアウトには ErrBusy を包んで応答する。取得した
// ファイルの Flock(LOCK_UN) と Close は呼び出し側の責務である。エラー時はファイルを
// クローズしてから返す。
//
// 親ディレクトリの作成と呼び出し前の ctx チェックは、エラーメッセージの文脈が
// 呼び出し側（状態ファイルロックとリポジトリ操作ロック）で異なるため、呼び出し側が
// 行う。状態ファイルロック（File.acquire）と repo 操作ロック（statedir）はどちらも
// この一つの再試行意味論を共有する。
func AcquireLock(ctx context.Context, path string, how int, timeout time.Duration) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock file %s: %w", path, err)
	}
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Flock(int(file.Fd()), how|syscall.LOCK_NB)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = file.Close()
			return nil, fmt.Errorf("acquire lock on %s: %w", path, err)
		}
		if time.Now().After(deadline) {
			_ = file.Close()
			return nil, fmt.Errorf("%w（%s のロックを %v 待っても取得できませんでした）", ErrBusy, path, timeout)
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-time.After(lockRetryInterval):
		}
	}
}

// Load はドキュメントを読み込む。ファイルが存在しない場合はデフォルトとして扱う。
// ドキュメントが Versioned を実装する場合、ストアに宣言されたバージョンより新しい
// バージョンはエラーとして拒否する。
func (s *Session[T]) Load() (*T, error) {
	f := s.file
	doc := f.factory()
	data, err := os.ReadFile(f.path)
	if errors.Is(err, fs.ErrNotExist) {
		return doc, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s file %s: %w", f.noun, f.path, err)
	}
	// （設定ローダーと同様に）未知のキーを拒否する。認識されないキーは、
	// そうしないと次回の保存時に黙って失われてしまう。
	md, err := toml.Decode(string(data), doc)
	if err != nil {
		return nil, fmt.Errorf("parse %s file %s: %w", f.noun, f.path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf("parse %s file %s: unknown key %q", f.noun, f.path, undecoded[0].String())
	}
	if v, ok := any(doc).(Versioned); ok && v.DocVersion() > f.version {
		return nil, fmt.Errorf(
			"%s file %s has version %d, newer than this build supports (%d); update worktree-integrator",
			f.noun, f.path, v.DocVersion(), f.version)
	}
	return doc, nil
}

// Save はドキュメントをアトミックに書き込む（一時ファイル + リネーム）。
// 読み取り専用（Shared）セッションでは ErrReadOnly で失敗する。
func (s *Session[T]) Save(doc *T) error {
	f := s.file
	if s.readOnly {
		return fmt.Errorf("save %s file %s: %w", f.noun, f.path, ErrReadOnly)
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(doc); err != nil {
		return fmt.Errorf("serialize %s: %w", f.noun, err)
	}
	tmp := f.tmpPath()
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, f.path); err != nil {
		return fmt.Errorf("replace %s: %w", f.path, err)
	}
	return nil
}

// Close はアドバイザリロックを解放し、ロックファイルをクローズする。
func (s *Session[T]) Close() error {
	_ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
	return s.lock.Close()
}

// Update は排他セッションの下で、読み込んだドキュメントに対して mutate を実行し、
// mutate が dirty を報告した場合にのみ結果を永続化する。これは単純な変更で共有される
// 「読み込み → 変更 → 書き込み」のサイクルである。逐次的に永続化する必要がある
// 呼び出し側は、代わりに Exclusive でセッションを保持して Load / Save を直接呼び出す。
func (f *File[T]) Update(ctx context.Context, mutate func(doc *T) (dirty bool, err error)) error {
	session, err := f.Exclusive(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = session.Close() }()
	doc, err := session.Load()
	if err != nil {
		return err
	}
	dirty, err := mutate(doc)
	if err != nil {
		return err
	}
	if dirty {
		return session.Save(doc)
	}
	return nil
}

// View は共有セッションの下で読み込んだドキュメントに対して view を実行する
// 読み取り専用の操作で、決して永続化しない。手書きのセッション管理の定型を
// 呼び出し側から取り除き、ロック解放漏れを防ぐ。ドキュメントを変更する場合は
// Update を使うこと。
func (f *File[T]) View(ctx context.Context, view func(doc *T) error) error {
	session, err := f.Shared(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = session.Close() }()
	doc, err := session.Load()
	if err != nil {
		return err
	}
	return view(doc)
}
