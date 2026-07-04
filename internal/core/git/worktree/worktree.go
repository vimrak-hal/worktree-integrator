// Package worktree はリポジトリ単位の git 作業を担う。リモートから最新を fetch し、
// 新しいブランチ上に連結ワークツリーを作成する。さらに、多数のリポジトリを
// 一度に処理する並列ランナーと結果の集約も提供する。git 操作のみの単一責務であり、
// 追加ファイルのコピーは app/create の post-create ステップ（infra/fscopy）が担う。
//
// Git 操作はローカルの `git` コマンド経由で行うため（internal/git を参照）、
// ネイティブの git ライブラリはリンクしていない。
package worktree

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/vimrak-hal/worktree-integrator/internal/core/git"
)

// Status は単一リポジトリの処理結果のカテゴリを表す。
type Status int

const (
	// StatusCreated: 新しいワークツリーが正常に作成された。
	StatusCreated Status = iota
	// StatusSkipped: リポジトリは意図的に手を加えずに残された。
	StatusSkipped
	// StatusFailed: エラーによりワークツリーの作成ができなかった。
	StatusFailed
)

// Stage は処理がどの段階でスキップ・失敗したかを表す封印された列挙である。
// 旧実装の自由文字列 Reason を置き換え、表示層（adapter/render）が段階ごとの
// ユーザー向けラベルに変換する。
type Stage int

const (
	// StageNone: 段階情報なし（StatusCreated）。
	StageNone Stage = iota
	// StageDestination: 作成先の検査・整理。既存 worktree の検出によるスキップ、
	// 作成先が占有されている・分類できない失敗がここに入る。
	StageDestination
	// StageBranchCheck: 同名ローカルブランチの存在確認。
	StageBranchCheck
	// StageFetch: リモートからの fetch。
	StageFetch
	// StageResolve: main / master の先端の解決。
	StageResolve
	// StagePrune: 孤立した worktree メタデータの掃除（git worktree prune）。
	StagePrune
	// StageAdd: worktree の作成（git worktree add）。
	StageAdd
	// StageCopy: 追加ファイルのコピー（app/create の post-create ステップ）。この
	// パッケージ自身は使わない。コピーの部分失敗は Created のまま Copy レポートで
	// 区別され、その段階の識別にこの値が使われる。
	StageCopy
	// StageCanceled: ctx のキャンセルにより着手されなかった（StatusSkipped）。
	StageCanceled
)

// Progress はリポジトリが作業を進める過程で報告されるリアルタイムの進捗状態を
// 表す、言語非依存の列挙である。途中経過（Fetching / Creating）のみを持ち、終端は
// Outcome に一本化されている。ユーザーに表示するラベルへの変換は表示層
// （adapter/render）が担う。
type Progress int

const (
	ProgressFetching Progress = iota
	ProgressCreating
)

// NoteKind は Reporter.Event で報告される途中経過イベントの種別を表す封印された
// 列挙である（旧 Reporter.Note の自由文字列を置き換えた）。ユーザー向けの文言は
// 表示層が持つ。
type NoteKind int

const (
	// NoteCopyRejected: 安全でないパスとしてコピー対象から除外した（Path、
	// app/create の post-create コピーステップが発する）。
	NoteCopyRejected NoteKind = iota
	// NoteCopyFailed: 1 つのパスのコピーに失敗した（Path, Err、post-create コピー
	// ステップが発する）。
	NoteCopyFailed
	// NoteGitignoreListFailed: gitignore 対象の列挙に失敗し、自動コピーを
	// スキップした（Err、post-create コピーステップが発する）。
	NoteGitignoreListFailed
	// NoteFetchDegraded: フェッチに失敗したが、既存の追跡ブランチ
	// （refs/remotes/<remote>/<branch>）が既にあるため、それを使って作成を続行する
	// （Err がフェッチ失敗の元となったエラー）。オフライン・ネットワーク不調時の
	// degrade で、Process 自身が発する。
	NoteFetchDegraded
)

// Note は 1 つの途中経過イベント。どのフィールドが意味を持つかは Kind に依存する。
type Note struct {
	Kind NoteKind
	// Path は対象の相対パス（コピー関連のイベント）。
	Path string
	// Err は元となったエラー（失敗イベント）。
	Err error
}

// Outcome は単一リポジトリの処理結果を表す。
type Outcome struct {
	// Repo はリポジトリのディレクトリ名。
	Repo string
	// Status は結果のカテゴリ。
	Status Status
	// Stage はスキップ・失敗が発生した段階（StatusCreated では StageNone）。
	Stage Stage
	// Err は失敗の元となったエラー（StatusFailed 以外では nil）。エラーは型のまま
	// 保持し、文字列化は表示層が表示の時点で行う。
	Err error
}

// AnyFailed は、いずれかのリポジトリが失敗したかどうかを返す。これは実行全体を
// 失敗とみなすかというワークフローの判断材料であり、結果の整形（表示層が担う）
// とは独立している。
func AnyFailed(results []Outcome) bool {
	for _, r := range results {
		if r.Status == StatusFailed {
			return true
		}
	}
	return false
}

// Request は単一リポジトリの作業内容を表す。
type Request struct {
	// RepoName はリポジトリのディレクトリ名。
	RepoName string
	// RepoPath は既存リポジトリへのパス。
	RepoPath string
	// WorktreeName はユーザーが指定したワークツリー名で、新しいブランチ名として
	// そのまま使われる（つまり "feature/login" は refs/heads/feature/login を作成する）。
	WorktreeName string
	// Target はワークツリーを作成するディレクトリ。
	Target string
	// Remote は fetch 元のリモート（例: "origin"）。
	Remote string
	// Base はベースブランチの指定。AutoBase（"auto"、ゼロ値の "" も同義）なら
	// リモートのデフォルトブランチ（symbolic-ref → main → master）を自動解決する。
	// それ以外は明示されたブランチ名として扱う。
	Base string
}

// AutoBase は Request.Base の特殊値で、リモートのデフォルトブランチ
// （git.DefaultBranch: symbolic-ref → main → master）を自動解決することを示す。
// ゼロ値の "" も同義に扱う（Request をリテラルで組み立てる既存呼び出し側との
// 互換のため）。
const AutoBase = "auto"

// Reporter は呼び出し側がリアルタイムの進捗を描画できるよう、途中経過の状態遷移
// （Update）と型付きイベント（Event）を受け取る。実装は並行利用に対して安全で
// なければならない。終端（完了・スキップ・失敗）は報告されず、Outcome に一本化
// されている。
type Reporter interface {
	Update(repo string, state Progress)
	Event(repo string, n Note)
}

// NopReporter は何も報告しない Reporter。進捗の通知先を持たない呼び出し側
// （テストや進捗表示を持たないフロントエンド）が使う。
type NopReporter struct{}

func (NopReporter) Update(string, Progress) {}
func (NopReporter) Event(string, Note)      {}

// Process はリモートのベースブランチ（req.Base、"auto" ならデフォルトブランチを
// 自動解決）の最新コミットを fetch し、単一リポジトリの連結ワークツリーを作成する。
// エラーを返すことはなく、すべての結果は返り値の Outcome に格納される。これにより
// 多数のリポジトリを並列処理する呼び出し側は、他の処理を継続できる。ctx のキャンセルは
// 実行中の git プロセスを終了させ、その段階の失敗（StatusFailed）として Outcome に
// 現れる。fetch 失敗は、既存の追跡ブランチがあれば NoteFetchDegraded を発して続行する
// （オフライン degrade）。
func Process(ctx context.Context, req Request, reporter Reporter) Outcome {
	report := func(state Progress) { reporter.Update(req.RepoName, state) }
	fail := func(stage Stage, err error) Outcome {
		return Outcome{Repo: req.RepoName, Status: StatusFailed, Stage: stage, Err: err}
	}

	// 作成先に既に存在するものから何をすべきかを判断する。分類そのものの失敗
	//（権限エラーなど）は握り潰さず表面化させる。
	dest, err := classifyDestination(req.Target)
	if err != nil {
		return fail(StageDestination, err)
	}
	switch dest {
	case destVacant:
		// 続行する
	case destEmptyDir:
		// git はワークツリーのディレクトリを自身で作成するため、まず空の
		// プレースホルダを削除する。削除失敗は握り潰さず表面化させる。
		if err := os.Remove(req.Target); err != nil {
			return fail(StageDestination,
				fmt.Errorf("remove empty directory %s: %w", req.Target, err))
		}
	case destWorktree:
		return Outcome{Repo: req.RepoName, Status: StatusSkipped, Stage: StageDestination}
	case destOccupied:
		return fail(StageDestination,
			fmt.Errorf("%s already exists and is not a worktree", req.Target))
	}

	// 同名のローカルブランチが既に存在する場合、暗黙に再利用するのではなく
	// コンフリクトとして扱う。ネットワーク fetch の前にチェックすることで、
	// オフラインかつ高速に失敗する。
	exists, err := git.LocalBranchExists(ctx, req.RepoPath, req.WorktreeName)
	if err != nil {
		return fail(StageBranchCheck, err)
	}
	if exists {
		return fail(StageBranchCheck,
			fmt.Errorf("local branch %q already exists; delete it or choose another name", req.WorktreeName))
	}

	// ベースブランチ名の解決: 明示（req.Base）が無ければリモートのデフォルトブランチを
	// 自動解決する（symbolic-ref → main → master）。この解決自体はローカルの ref のみを
	// 見るため、ネットワークアクセスは発生しない。
	branch := req.Base
	if branch == "" || branch == AutoBase {
		resolved, err := git.DefaultBranch(ctx, req.RepoPath, req.Remote)
		if err != nil {
			return fail(StageResolve, err)
		}
		branch = resolved
	}

	report(ProgressFetching)
	// fetch は branch 1 本だけに限定する（高速化）。失敗しても即座に Failed とはせず、
	// 既存の追跡ブランチ（refs/remotes/<remote>/<branch>）があればそれを使って続行する
	// （オフライン degrade）。既存 ref も無ければここで Failed にする。
	if fetchErr := git.FetchRef(ctx, req.RepoPath, req.Remote, branch); fetchErr != nil {
		exists, existsErr := git.RemoteBranchExists(ctx, req.RepoPath, req.Remote, branch)
		if existsErr != nil {
			return fail(StageFetch, existsErr)
		}
		if !exists {
			return fail(StageFetch, fetchErr)
		}
		reporter.Event(req.RepoName, Note{Kind: NoteFetchDegraded, Err: fetchErr})
	}

	oid, err := git.ResolveTip(ctx, req.RepoPath, req.Remote, branch)
	if err != nil {
		return fail(StageResolve, err)
	}

	report(ProgressCreating)
	// 手作業で削除されたワークツリーディレクトリはメタデータを残すため、
	// 名前が衝突せず再利用できるよう、古いエントリを削除する。
	if err := git.PruneWorktrees(ctx, req.RepoPath); err != nil {
		return fail(StagePrune, err)
	}
	if err := git.AddWorktree(ctx, req.RepoPath, req.WorktreeName, req.Target, oid); err != nil {
		return fail(StageAdd, err)
	}

	return Outcome{Repo: req.RepoName, Status: StatusCreated}
}

// destination はワークツリーの作成先パスに既に存在するものを表す。
type destination int

const (
	destVacant   destination = iota // 何もない — 通常どおり作成する
	destEmptyDir                    // 空のディレクトリ — 削除して再利用しても安全
	destWorktree                    // 既存のチェックアウト — すでに完了している
	destOccupied                    // 別の内容 — 上書きせず拒否する
)

// classifyDestination は path に存在するものを分類する。".git" エントリは
// 既存のチェックアウトを示し、それ以外の空でない内容は別物として扱う。
// 「存在しない」は fs.ErrNotExist で厳密に判別し、それ以外の stat / 読み取りの
// 失敗（権限エラーなど）は「存在しない」や「占有」と混同せずエラーとして返す。
func classifyDestination(path string) (destination, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return destVacant, nil // 存在しない — 通常どおり作成する
	}
	if err != nil {
		return 0, fmt.Errorf("inspect destination %s: %w", path, err)
	}
	if !info.IsDir() {
		return destOccupied, nil // ワークツリーのディレクトリがあるべき場所にファイルがある
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return 0, fmt.Errorf("read destination %s: %w", path, err)
	}
	if len(entries) == 0 {
		return destEmptyDir, nil
	}
	if git.IsWorkTree(path) {
		return destWorktree, nil
	}
	return destOccupied, nil
}
