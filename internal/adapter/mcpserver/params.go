package mcpserver

import (
	"github.com/vimrak-hal/worktree-integrator/internal/app/action"
)

// ----- パラメータスキーマ -----
//
// かつて全ツールが受けていた repos_dir / worktrees_dir のオーバーライドは廃止した
// （意図的な仕様変更）。ディレクトリは設定ファイル（と MCP サーバープロセスの環境
// 変数）を唯一の真実とし、LLM がツール呼び出しごとに別のディレクトリを指す経路を
// 塞ぐ。

// CreateParams は worktree_create のパラメータ。
type CreateParams struct {
	WorktreeName string   `json:"worktree_name" jsonschema:"The worktree name, also used as the branch name. Letters, digits, '.', '_' and hyphens, optionally split into segments by '/' for a hierarchical name (e.g. feature/login). Segments must not be '.'/'..', start with '-' or '.', or end with '.lock'."`
	Repos        []string `json:"repos" jsonschema:"The repository names to create the worktree in (see repos_list). Must be non-empty; unknown names are an error."`
	Base         string   `json:"base,omitempty" jsonschema:"Override the base branch/ref to create from (defaults to repos.<repo>.base / defaults.base / auto-detecting the remote's default branch)."`
	Remote       string   `json:"remote,omitempty" jsonschema:"Override the remote to fetch from (defaults to origin)."`
	Concurrency  int      `json:"concurrency,omitempty" jsonschema:"Cap the number of repositories processed in parallel (0 = automatic)."`
}

// ServerSwitchParams は server_switch のパラメータ。
type ServerSwitchParams struct {
	WorktreeName    string   `json:"worktree_name" jsonschema:"The worktree whose servers to activate."`
	Repos           []string `json:"repos,omitempty" jsonschema:"Limit to these repositories; empty means every configured repository."`
	RequireWorktree bool     `json:"require_worktree,omitempty" jsonschema:"Error (instead of skipping) when a repository's worktree is missing."`
	Restart         bool     `json:"restart,omitempty" jsonschema:"Restart even if the requested worktree's server is already running."`
}

// ServerScopeParams は server_status のパラメータ。
type ServerScopeParams struct {
	Repos []string `json:"repos,omitempty" jsonschema:"Limit to these repositories; empty means every configured repository."`
}

// ServerStopParams は server_stop のパラメータ。
type ServerStopParams struct {
	WorktreeName *string  `json:"worktree_name,omitempty" jsonschema:"Only stop servers running for this worktree; omit to stop all. An empty string is an error, not 'all'."`
	Repos        []string `json:"repos,omitempty" jsonschema:"Limit to these repositories; empty means every configured repository."`
}

// ServerLogsParams は server_logs のパラメータ。
type ServerLogsParams struct {
	WorktreeName *string  `json:"worktree_name,omitempty" jsonschema:"View this worktree's logs; omit for the currently running servers'. An empty string is an error, not 'all'."`
	Repos        []string `json:"repos,omitempty" jsonschema:"Limit to these repositories; empty means every configured repository."`
	Lines        int      `json:"lines,omitempty" jsonschema:"Number of trailing lines to show (default 50, clamped to at most 2000)."`
	Prev         bool     `json:"prev,omitempty" jsonschema:"Read the previous generation of the log instead (rotated aside when the server was last started)."`
}

// WorktreeRemoveParams は worktree_remove のパラメータ。CLI の --force に相当する
// パラメータは意図的に存在しない: dirty なチェックアウトはエラーとして返るのみで、
// LLM に強制削除を許さない（意図的な非公開）。
type WorktreeRemoveParams struct {
	Name       string `json:"name" jsonschema:"The worktree name to remove (see worktree_list)."`
	KeepBranch bool   `json:"keep_branch,omitempty" jsonschema:"Keep the branch instead of deleting it along with the worktree."`
}

// AliasSetParams は alias_set のパラメータ。
type AliasSetParams struct {
	WorktreeName string `json:"worktree_name" jsonschema:"The worktree (and branch) name the alias is keyed by."`
	Alias        string `json:"alias" jsonschema:"The label to display. Must be non-empty; use alias_remove to clear."`
}

// AliasNameParams は alias_remove のパラメータ。
type AliasNameParams struct {
	WorktreeName string `json:"worktree_name" jsonschema:"The worktree name to look up."`
}

// NoParams は repos_list / alias_list 用の空のパラメータセット。
type NoParams struct{}

// ----- パラメータの解釈 -----

// serverLogsLineLimit は server_logs が一度に返す最大行数。MCP クライアントの
// コンテキストを巨大なログで溢れさせないための上限である。ツール説明文
// （mcpserver.go）はこの定数から fmt.Sprintf で生成されるが、ServerLogsParams.Lines の
// jsonschema タグは構造体タグが文字列リテラルのため定数を埋め込めない。タグの
// 「clamped to at most 2000」はこの定数と手動で同期すること（説明文・タグと同期）。
const serverLogsLineLimit = 2000

// defaultLogLines は lines 省略時の既定行数。serverLogsLineLimit と同様、ツール説明文は
// この定数から生成されるが、jsonschema タグの「default 50」は文字列リテラルのため
// 手動で同期すること（説明文・タグと同期）。
const defaultLogLines = 50

// clampLines は server_logs の行数を [既定 50, 上限 2000] に収める。0 以下
// （省略を含む）は既定へ、上限超過は上限へクランプする。
func clampLines(lines int) int {
	if lines <= 0 {
		return defaultLogLines
	}
	return min(lines, serverLogsLineLimit)
}

// scopeFromPtr は省略可能な worktree 名ポインタを WorktreeScope に変換する。
// nil（パラメータ省略）のみが「全 worktree 対象」を意味する。明示的な空文字列は
// （旧実装のように全件へ正規化せず）不正な名前としてエラーになる — 意図的な仕様変更。
// LLM が「空文字列 = 全件」という暗黙の規約に頼ることを許さない。
func scopeFromPtr(p *string) (action.WorktreeScope, error) {
	if p == nil {
		return action.AllWorktrees{}, nil
	}
	name, err := action.ParseName(*p)
	if err != nil {
		return nil, err
	}
	return action.OneWorktree{Name: name}, nil
}
