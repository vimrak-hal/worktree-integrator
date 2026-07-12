package create

import (
	"fmt"

	"github.com/vimrak-hal/worktree-integrator/internal/core/git/worktree"
	"github.com/vimrak-hal/worktree-integrator/internal/core/hooks"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/fscopy"
)

// Result は create ワークフローの結果である。CLI のテキスト描画（adapter/render）、
// `--json`、MCP の structuredContent がすべてこの 1 つの型から派生するため、
// フィールドはドメインの列挙をそのまま持たず、JSON スキーマとして表現可能な
// 文字列の語彙（Disposition / RepoOutcome.Status / Stage など）に写して保持する。
// io.Writer への直書きは存在しない。
type Result struct {
	// Worktree は要求された worktree（およびブランチ）名。
	Worktree string `json:"worktree"`
	// Root は worktree のルートディレクトリ（<worktrees_dir>/<worktree>）。
	Root string `json:"root"`
	// ReposDir は探索したリポジトリのベースディレクトリ。
	ReposDir string `json:"repos_dir"`
	// Disposition はワークフローがどの形で終わったか。
	Disposition Disposition `json:"disposition"`
	// Discovered は repos_dir 配下で探索されたリポジトリ数（探索前に終わった場合 0）。
	Discovered int `json:"discovered"`
	// Repos はリポジトリごとの結果（作成を実行した場合のみ）。
	Repos []RepoOutcome `json:"repos,omitempty"`
	// Hooks は実行されたライフサイクルフックの結果（実行順）。
	Hooks []hooks.Report `json:"hooks,omitempty"`
	// Created / Skipped / Failed は Repos の集計。
	Created int `json:"created"`
	Skipped int `json:"skipped"`
	Failed  int `json:"failed"`
	// SetupInvalidateError は、新規作成リポジトリの古い setup 記録の無効化に失敗した
	// 場合のエラー文字列（致命的ではない警告。表示は render が行う）。
	SetupInvalidateError string `json:"setup_invalidate_error,omitempty"`
}

// Disposition は create ワークフローの終わり方を表す文字列の語彙。
type Disposition string

const (
	// DispositionCreated: 作成を実行した（部分失敗を含む。集計は Created/Skipped/Failed）。
	DispositionCreated Disposition = "created"
	// DispositionNothingToAdd: 対話選択モードで、探索された全リポジトリがこの
	// worktree のメンバーとして既に存在していた（差分作成の候補が無い）。after
	// フック（作成完了/遷移フック）のみ実行して正常終了した。
	DispositionNothingToAdd Disposition = "nothing_to_add"
	// DispositionNoRepos: repos_dir にリポジトリが 1 つも見つからなかった。
	DispositionNoRepos Disposition = "no_repositories"
	// DispositionNothingSelected: 対話選択で 1 つも選ばれなかった。
	DispositionNothingSelected Disposition = "nothing_selected"
	// DispositionAborted: before フックの失敗により、リポジトリに触れる前に中断した。
	DispositionAborted Disposition = "aborted"
)

// RepoOutcome は 1 リポジトリの結果。worktree.Outcome の JSON 表現に、post-create
// コピーステップの結果（Copy）を加えたもの。
type RepoOutcome struct {
	Repo string `json:"repo"`
	// Status は "created" | "skipped" | "failed"。
	Status string `json:"status"`
	// Stage はスキップ・失敗が発生した段階の識別子（"fetch" など）。コピーが部分
	// 失敗した Created では "copy" になる（Status は created のまま — 区別は Copy
	// レポートが担う）。
	Stage string `json:"stage,omitempty"`
	// Error は失敗の元となったエラーの文字列（表示・シリアライズ用）。
	Error string `json:"error,omitempty"`
	// Copy は post-create コピーステップの結果（コピー設定がある Created のみ）。
	Copy *CopyReport `json:"copy,omitempty"`
}

// RepoOutcome.Status の語彙。
const (
	RepoCreated = "created"
	RepoSkipped = "skipped"
	RepoFailed  = "failed"
)

// CopyReport は fscopy.Report の JSON 表現。
type CopyReport struct {
	Copied   []string      `json:"copied,omitempty"`
	Rejected []string      `json:"rejected,omitempty"`
	Failures []CopyFailure `json:"failures,omitempty"`
}

// CopyFailure は 1 つのコピー失敗。
type CopyFailure struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// statusID は worktree.Status を JSON の語彙へ写す。封印された列挙のため、未知の
// 値はバグでありパニックさせる。
func statusID(s worktree.Status) string {
	switch s {
	case worktree.StatusCreated:
		return RepoCreated
	case worktree.StatusSkipped:
		return RepoSkipped
	case worktree.StatusFailed:
		return RepoFailed
	default:
		panic(fmt.Sprintf("unknown worktree.Status %d", s))
	}
}

// StageID は worktree.Stage を JSON の語彙へ写す。表示層（render）はこの識別子を
// キーに日本語ラベルへ変換する。封印された列挙のため、未知の値はバグであり
// パニックさせる。
func StageID(s worktree.Stage) string {
	switch s {
	case worktree.StageNone:
		return ""
	case worktree.StageDestination:
		return "destination"
	case worktree.StageBranchCheck:
		return "branch_check"
	case worktree.StageFetch:
		return "fetch"
	case worktree.StageResolve:
		return "resolve"
	case worktree.StagePrune:
		return "prune"
	case worktree.StageAdd:
		return "add"
	case worktree.StageCopy:
		return "copy"
	case worktree.StageCanceled:
		return "canceled"
	default:
		panic(fmt.Sprintf("unknown worktree.Stage %d", s))
	}
}

// appendHookOutcomes は 1 タイミング分のフック結果を表示・シリアライズ用の Report に
// 写して追記する。表示語彙（hooks.Report）は core/hooks が所有する。
func appendHookOutcomes(dst []hooks.Report, timing string, outcomes []hooks.Outcome) []hooks.Report {
	return append(dst, hooks.Reports(timing, outcomes)...)
}

// copyReportDTO は fscopy.Report を JSON 表現へ写す。nil レポート（コピー設定なし）は
// nil のまま返す。
func copyReportDTO(r *fscopy.Report) *CopyReport {
	if r == nil {
		return nil
	}
	dto := &CopyReport{Copied: r.Copied, Rejected: r.Rejected}
	for _, f := range r.Failures {
		dto.Failures = append(dto.Failures, CopyFailure{Path: f.Path, Error: f.Err.Error()})
	}
	return dto
}
