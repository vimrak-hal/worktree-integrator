package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/vimrak-hal/worktree-integrator/internal/infra/proc"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/statedir"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/store"
)

// StateVersion は servers.toml のオンディスクフォーマットバージョン。v2 で
// RepoState.Active と Runtime.Initialized を廃止し、worktree を Instance の属性に
// 一本化した。旧形式（v1 以前）はマイグレーションせず .bak へ退避する（migrateLegacy）。
const StateVersion uint32 = 2

// nowUnix は現在時刻を Unix エポックからの秒数として返す。これはサーバーの起動時刻や
// setup 完了時刻が永続化される形式である。
func nowUnix() int64 {
	return time.Now().Unix()
}

// State は、永続化されるサーバー状態の全体。
//
// 不変条件（v2）: 「どの worktree がアクティブか」という独立した状態は存在しない。
// それは Runtime.Running.Worktree（稼働インスタンスの属性）からの導出値である。
// 状態の重複が無いため、部分失敗しても「虚偽のアクティブ表示」は構造的に起こらない。
type State struct {
	Version uint32                `toml:"version"`
	Repos   map[string]*RepoState `toml:"repos"`
}

// DocVersion は store.Versioned を実装する。Load 時に、このビルドが理解できるより
// 新しいフォーマットのファイルを拒否するために使われる。
func (s *State) DocVersion() uint32 { return s.Version }

// Repo は repo の状態を返し、存在しなければ空のエントリを作成する。
func (s *State) Repo(name string) *RepoState {
	if s.Repos == nil {
		s.Repos = map[string]*RepoState{}
	}
	rs := s.Repos[name]
	if rs == nil {
		rs = &RepoState{Servers: map[string]*Runtime{}}
		s.Repos[name] = rs
	}
	return rs
}

// RepoState は、1 つのリポジトリのサーバー状態。サーバー名をキーとしたサーバー
// ごとのランタイム状態のみを持つ（旧 v1 の Active は廃止された。どの worktree で
// 動いているかは各サーバーの Instance.Worktree が持つ）。
type RepoState struct {
	Servers map[string]*Runtime `toml:"servers"`
}

// Server は名前付きサーバーの runtime を返し、存在しなければ空のエントリを作成する。
func (r *RepoState) Server(name string) *Runtime {
	if r.Servers == nil {
		r.Servers = map[string]*Runtime{}
	}
	rt := r.Servers[name]
	if rt == nil {
		rt = &Runtime{}
		r.Servers[name] = rt
	}
	return rt
}

// Runtime は、リポジトリ内における 1 つのサーバーのランタイム状態。TOML の
// シリアライズではフィールドの順序が重要で、スカラー（last_log）→ setup サブテーブル →
// running サブテーブルの順に書き出される。
//
// 不変条件:
//   - Running が非 nil のとき、そのインスタンスは「最後に起動を確認したプロセス」で
//     ある。生存はプロセス実体（Ident の開始時刻照合）と突き合わせて検証され、
//     消滅していれば Probe が自己修復する。停止に失敗した場合は Running を保持する
//     （孤児を台帳から消さない。次回コマンドで再試行できる）。
//   - Setup の記録は「その worktree でこのサーバーの setup が完了した」ことを表すが、
//     判定時には record.Path の実在とも突き合わせる（worktree が消えていれば初回扱い）。
type Runtime struct {
	// LastLog は最後に起動したインスタンスのログパス。Running が消えた後（クラッシュ・
	// 停止後）でも `server logs` が直近のログへフォールバックできるようにする。
	LastLog string `toml:"last_log,omitempty"`
	// Setup は、worktree 名 → setup 完了の記録。
	Setup map[string]SetupRecord `toml:"setup,omitempty"`
	// Running は、稼働中の（または最後に稼働が確認された）プロセス（あれば）。
	Running *Instance `toml:"running,omitempty"`
}

// RecordSetup は worktree の setup 完了を記録する。path は setup を実行した
// worktree 内のリポジトリルートで、isFirst 判定時に実在確認される。
func (r *Runtime) RecordSetup(worktree, path string) {
	if r.Setup == nil {
		r.Setup = map[string]SetupRecord{}
	}
	r.Setup[worktree] = SetupRecord{Path: path, DoneAt: nowUnix()}
}

// SetupRecord は、1 つの worktree に対する setup 完了の記録。
type SetupRecord struct {
	// Path は setup を実行した worktree 内のリポジトリルート。実在しなくなっていれば
	// （worktree が削除・再作成されていれば）記録は無効として扱われる。
	Path   string `toml:"path"`
	DoneAt int64  `toml:"done_at"`
}

// Instance は、稼働中のサーバープロセス。
type Instance struct {
	// Ident はプロセスの同一性トークン。シグナル送出前に必ず開始時刻を照合し、
	// pgid 再利用による無関係プロセスの誤殺を防ぐ。
	Ident proc.Ident `toml:"ident"`
	// Worktree はこのインスタンスが動いている worktree 名。リポジトリ単位の
	// 「アクティブ worktree」はこのフィールドからの導出値である。
	Worktree string `toml:"worktree"`
	// Log はこのインスタンスの出力を取り込んでいるログファイル。読み出しはこの
	// 記録値を正とし、決定的パスの再計算はフォールバックである。
	Log string `toml:"log"`
	// GraceSecs は起動時点の Spec.Grace() を凍結した値。停止時には（設定がその後
	// 変わっていても）この値を使う。
	GraceSecs uint64 `toml:"grace_secs"`
	StartedAt int64  `toml:"started_at"`
}

// Grace は、このインスタンスの停止時に SIGTERM → SIGKILL のエスカレートまで待つ
// 猶予（起動時に凍結された値）。
func (i *Instance) Grace() time.Duration {
	return time.Duration(i.GraceSecs) * time.Second
}

// StateStore は、statedir.Root の下でアドバイザリロックのもと State を読み書きする。
// 共有の store プリミティブの上に、ログパスの導出と旧形式ファイルの退避を重ねる。
// ログディレクトリの作成はここでは行わない（プロセス起動経路 SpawnDetached が担う）。
type StateStore struct {
	inner *store.File[State]
	root  statedir.Root
	// OnLegacy は旧形式（v1 以前）の状態ファイルを .bak へ退避した直後に、その退避先
	// パスとともに呼ばれる。表示層が警告（「稼働中のサーバーは手動で停止してください」）
	// を出すための通知であり、nil なら何もしない。
	OnLegacy func(bakPath string)
	// migrateOnce / migrateErr は、レガシー移行の検査をこのストアインスタンスあたり
	// 高々一度に絞る。移行はディスク上のファイルを一度退避すれば意味を持ち終えるため、
	// Exclusive / Update / View が毎回検査を繰り返す必要はない。最初の検査の結果
	//（成功なら nil）を保持し、以降の全経路で再利用する。ストアはワークフロー単位で
	// 生成される（app 層が呼び出しごとに NewStateStore する）ため、保持したエラーが
	// 別コマンドへ波及することはない。
	migrateOnce sync.Once
	migrateErr  error
}

// NewStateStore は root を状態ルートとする状態ストアを返す。呼び出し側（app 層）が
// statedir.Root を解決して注入することで、alias ストアとのディレクトリ共有が
// 明示的な契約になる。
func NewStateStore(root statedir.Root) *StateStore {
	inner := store.New(root.ServersFile(), "state", StateVersion, func() *State {
		return &State{Version: StateVersion, Repos: map[string]*RepoState{}}
	})
	return &StateStore{inner: inner, root: root}
}

// StateFile は、状態ファイルのパス（テストで使用）。
func (s *StateStore) StateFile() string { return s.inner.Path() }

// LogsDir は、サーバーごとのログファイルを保持するディレクトリ。
func (s *StateStore) LogsDir() string { return s.root.LogsDir() }

// legacyProbe は旧形式検出のためにバージョンだけを読むための最小のデコード先。
// 未知キーは無視する（旧形式は active / initialized キーを含むため、State への
// 厳密デコードでは読めない）。
type legacyProbe struct {
	Version uint32 `toml:"version"`
}

// ensureMigrated はレガシー移行の検査を、このストアインスタンスあたり高々一度だけ
// 実行する。最初の呼び出しの結果（成功なら nil、失敗ならそのエラー）を保持し、
// 以降の Exclusive / Update / View はロックも読み取りも取らずにそれを再利用する。
func (s *StateStore) ensureMigrated(ctx context.Context) error {
	s.migrateOnce.Do(func() {
		s.migrateErr = s.migrateLegacy(ctx)
	})
	return s.migrateErr
}

// migrateLegacy は状態ファイルが旧形式（version が StateVersion 未満）であれば
// <path>.bak へ退避する。マイグレーション機構は作らない（設計判断）: 退避後は
// 新規の空状態から始まり、旧ファイルに記録されていた稼働中サーバーは手動での停止が
// 必要になる（OnLegacy 経由で表示層が警告する）。
//
// まず共有ロックの下で版だけを覗き、旧形式でない一般的な場合は排他ロックを取らずに
// 読み取り専用で済ませる（読み取り専用の View が排他ロックを一瞬掴む問題を避ける）。
// 旧形式を検出したときだけ排他ロックへ昇格し、再確認のうえ退避する。退避は排他ロックの
// 下で行うため並行するコマンドと競合しない。すべての公開エントリポイント（Exclusive /
// Update / View）が最初にこれ（を包む ensureMigrated）を通るため、旧形式ファイルが
// State のデコードに達する経路は存在しない。
func (s *StateStore) migrateLegacy(ctx context.Context) error {
	// 共有ロックの下で版を覗く。旧形式でなければここで終える（読み取り専用のまま）。
	shared, err := s.inner.Shared(ctx)
	if err != nil {
		return err
	}
	legacy, err := s.isLegacyFile()
	// 排他ロックへ昇格する前に共有ロックを手放す（同一ロックファイルの共有と排他は
	// 同一プロセス内でも競合するため、保持したままでは昇格できない）。
	_ = shared.Close()
	if err != nil || !legacy {
		return err
	}

	// 旧形式を検出した場合のみ排他ロックへ昇格する。共有を手放してから排他を取る間に
	// 別プロセスが退避している可能性があるため、排他の下でもう一度確認する。
	session, err := s.inner.Exclusive(ctx)
	if err != nil {
		return err
	}
	defer session.Close()
	if legacy, err := s.isLegacyFile(); err != nil || !legacy {
		return err
	}
	bak := s.inner.Path() + ".bak"
	if err := os.Rename(s.inner.Path(), bak); err != nil {
		return fmt.Errorf("move legacy state file aside: %w", err)
	}
	if s.OnLegacy != nil {
		s.OnLegacy(bak)
	}
	return nil
}

// isLegacyFile は状態ファイルの版だけを緩くデコードして読み、旧形式（StateVersion
// 未満）かどうかを返す。ファイルが無い・デコードできない場合は「旧形式ではない」と
// して扱う（デコード不能なファイルはここでは触らず、後続の Load に本来のエラーを
// 報告させる）。呼び出し側がロック（共有または排他）を保持していることが前提。
func (s *StateStore) isLegacyFile() (bool, error) {
	data, err := os.ReadFile(s.inner.Path())
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read state file %s: %w", s.inner.Path(), err)
	}
	var probe legacyProbe
	if _, err := toml.Decode(string(data), &probe); err != nil {
		return false, nil
	}
	return probe.Version < StateVersion, nil
}

// Exclusive は排他ロックを取得し、読み書き可能なセッションを返す。ロックは短命に
// 保つこと: ワークフロー全体の直列化は statedir.Root.WithRepoLock（repo 操作ロック）が
// 担い、状態ファイルロックは Load / Save の間だけ保持する。
func (s *StateStore) Exclusive(ctx context.Context) (*store.Session[State], error) {
	if err := s.ensureMigrated(ctx); err != nil {
		return nil, err
	}
	return s.inner.Exclusive(ctx)
}

// Update は、排他ロックの下で読み込んだ状態に対して mutate を実行し、状態が dirty と
// 報告された場合にのみ永続化する。
func (s *StateStore) Update(ctx context.Context, mutate func(state *State) (dirty bool, err error)) error {
	if err := s.ensureMigrated(ctx); err != nil {
		return err
	}
	return s.inner.Update(ctx, mutate)
}

// View は、共有ロックの下で読み込んだ状態に対して view を実行する読み取り専用の
// 操作で、決して永続化しない。状態を変更する場合は Update を使うこと。view に渡る
// ドキュメントは呼び出しごとに新しく読まれた専有のコピーであり、view の外へ持ち出して
// も安全である（ただしそれを書き戻す場合は Update を使う）。
func (s *StateStore) View(ctx context.Context, view func(state *State) error) error {
	if err := s.ensureMigrated(ctx); err != nil {
		return err
	}
	return s.inner.View(ctx, view)
}
