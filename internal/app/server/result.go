package server

import (
	"fmt"

	coreserver "github.com/vimrak-hal/worktree-integrator/internal/core/server"
)

// このファイルは server ワークフロー群の型付き Result を定義する。CLI のテキスト
// 描画（adapter/render）、`--json`、MCP の structuredContent がすべて同じ型から
// 派生するため、フィールドはドメインの列挙をそのまま持たず、JSON スキーマとして
// 表現可能な文字列の語彙に写して保持する。io.Writer への直書きは存在しない。

// SwitchResult は `server switch` の結果。
type SwitchResult struct {
	// PerServer は対象となった（repo × server）ごとの結果（処理順）。
	PerServer []ServerOutcome `json:"servers,omitempty"`
	// Started / Already / Skipped / Failed は PerServer の集計。
	Started int `json:"started"`
	Already int `json:"already"`
	Skipped int `json:"skipped"`
	Failed  int `json:"failed"`
	// UnknownRepos は --repo / repos で指定されたが、設定にも状態にも存在しない
	// リポジトリ名（警告扱い）。
	UnknownRepos []string `json:"unknown_repos,omitempty"`
	// NoServerConfig は、対象が空でありかつサーバー設定自体が空だったことを示す
	// （表示層が [servers.*] の設定を案内する）。
	NoServerConfig bool `json:"no_server_config,omitempty"`
	// LegacyBackup は旧形式の状態ファイルを退避した先のパス（発生時のみ）。
	LegacyBackup string `json:"legacy_state_backup,omitempty"`
}

// SetLegacyBackup は旧形式状態ファイルの退避先を記録する（App が退避の有無を写す）。
func (r *SwitchResult) SetLegacyBackup(bak string) { r.LegacyBackup = bak }

// tally は PerServer から集計フィールドを再計算する。
func (r *SwitchResult) tally() {
	r.Started, r.Already, r.Skipped, r.Failed = 0, 0, 0, 0
	for _, o := range r.PerServer {
		switch o.Status {
		case OutcomeStarted:
			r.Started++
		case OutcomeAlreadyRunning:
			r.Already++
		case OutcomeSkipped:
			r.Skipped++
		case OutcomeFailed:
			r.Failed++
		default:
			panic(fmt.Sprintf("unknown switch outcome status %q", o.Status))
		}
	}
}

// StopResult は `server stop` の結果。
type StopResult struct {
	// PerServer は停止を試みた（repo × server）ごとの結果。稼働記録の無い
	// サーバーは現れない。
	PerServer []ServerOutcome `json:"servers,omitempty"`
	// Stopped / Failed は PerServer の集計。
	Stopped int `json:"stopped"`
	Failed  int `json:"failed"`
	// UnknownRepos / LegacyBackup は SwitchResult と同じ。
	UnknownRepos []string `json:"unknown_repos,omitempty"`
	LegacyBackup string   `json:"legacy_state_backup,omitempty"`
}

// SetLegacyBackup は旧形式状態ファイルの退避先を記録する（App が退避の有無を写す）。
func (r *StopResult) SetLegacyBackup(bak string) { r.LegacyBackup = bak }

// StatusResult は `server status` の結果。
type StatusResult struct {
	// Rows は（repo × server）ごとの 1 行。
	Rows []Row `json:"rows,omitempty"`
	// UnknownRepos / NoServerConfig / LegacyBackup は SwitchResult と同じ。
	UnknownRepos   []string `json:"unknown_repos,omitempty"`
	NoServerConfig bool     `json:"no_server_config,omitempty"`
	LegacyBackup   string   `json:"legacy_state_backup,omitempty"`
}

// SetLegacyBackup は旧形式状態ファイルの退避先を記録する（App が退避の有無を写す）。
func (r *StatusResult) SetLegacyBackup(bak string) { r.LegacyBackup = bak }

// Row は status テーブルの 1 行。
type Row struct {
	Repo   string `json:"repo"`
	Server string `json:"server"`
	// Worktree はこのサーバーが動いている（または最後に動いていた記録のある）
	// worktree 名。無ければ空。
	Worktree string `json:"worktree,omitempty"`
	// Alias は Worktree の表示用別名。無ければ空。
	Alias string `json:"alias,omitempty"`
	// Pid は稼働中のプロセス ID（稼働中のみ非ゼロ）。
	Pid int `json:"pid,omitempty"`
	// State は "running" | "crashed" | "stopped"。
	State string `json:"state"`
}

// Row.State の語彙。
const (
	StateRunning = "running"
	StateCrashed = "crashed"
	StateStopped = "stopped"
)

// LogsResult は `server logs` の結果。
type LogsResult struct {
	// Logs は対象となったログごとのエントリ。
	Logs []LogEntry `json:"logs,omitempty"`
	// UnknownRepos / LegacyBackup は SwitchResult と同じ。
	UnknownRepos []string `json:"unknown_repos,omitempty"`
	LegacyBackup string   `json:"legacy_state_backup,omitempty"`
}

// SetLegacyBackup は旧形式状態ファイルの退避先を記録する（App が退避の有無を写す）。
func (r *LogsResult) SetLegacyBackup(bak string) { r.LegacyBackup = bak }

// LogEntry は 1 つのサーバーログの読み取り結果。
type LogEntry struct {
	Repo   string `json:"repo"`
	Server string `json:"server"`
	// Path はログファイルのパス。CLI の -f（tail -f）はこのパスを使う。
	Path string `json:"path"`
	// Missing はログファイルが存在しなかったことを示す（名前指定スコープのみ現れる）。
	Missing bool `json:"missing,omitempty"`
	// Lines は末尾 n 行（読み取れた場合）。
	Lines []string `json:"lines,omitempty"`
	// Error は読み取りエラーの文字列（発生時のみ）。
	Error string `json:"error,omitempty"`
}

// ServerOutcome は 1 つの（repo × server）操作の結果。
type ServerOutcome struct {
	Repo   string `json:"repo"`
	Server string `json:"server"`
	// Status は switch では "started" | "already_running" | "skipped" | "failed"、
	// stop では "stopped" | "stop_failed"。
	Status string `json:"status"`
	// Reason は Status="skipped" の理由（"no_server_config" | "missing_worktree"）。
	Reason string `json:"reason,omitempty"`
	// Path は Reason="missing_worktree" のときの探した worktree パス。
	Path string `json:"path,omitempty"`
	// Events は処理中に起きた事象（発生順）。
	Events []Event `json:"events,omitempty"`
	// Failure は Status="failed" の詳細。
	Failure *Failure `json:"failure,omitempty"`
}

// ServerOutcome.Status の語彙。
const (
	OutcomeStarted        = "started"
	OutcomeAlreadyRunning = "already_running"
	OutcomeSkipped        = "skipped"
	OutcomeFailed         = "failed"
	OutcomeStopped        = "stopped"
	OutcomeStopFailed     = "stop_failed"
)

// ServerOutcome.Reason の語彙。
const (
	ReasonNoServerConfig  = "no_server_config"
	ReasonMissingWorktree = "missing_worktree"
)

// Event は 1 つのサーバーイベントの JSON 表現。
type Event struct {
	// Kind は "already_running" | "stopping_old" | "started" | "stopped" |
	// "stop_failed" | "already_stopped"。
	Kind string `json:"kind"`
	// Pid は対象のプロセス ID（起動／停止イベント）。
	Pid int `json:"pid,omitempty"`
	// Error は stop_failed の元となったエラーの文字列。
	Error string `json:"error,omitempty"`
}

// Failure は switch の失敗の詳細。
type Failure struct {
	// Kind は "step"（ライフサイクルコマンド失敗）| "start"（起動失敗・即死）|
	// "stop"（旧インスタンスの停止失敗）| "other"。
	Kind string `json:"kind"`
	// Step は Kind="step" のコマンド名（setup / on_switch / on_activate）。
	Step string `json:"step,omitempty"`
	// Error はエラーの文字列。
	Error string `json:"error"`
	// LogTail は即死検出時のログ末尾（Kind="start" のみ）。
	LogTail []string `json:"log_tail,omitempty"`
}

// Failure.Kind の語彙。
const (
	FailStep  = "step"
	FailStart = "start"
	FailStop  = "stop"
	FailOther = "other"
)

// eventDTO は coreserver.Event を JSON 表現へ写す。封印された列挙のため、未知の
// 値はバグでありパニックさせる。
func eventDTO(ev coreserver.Event) Event {
	out := Event{Pid: ev.Pid}
	switch ev.Kind {
	case coreserver.EventAlreadyRunning:
		out.Kind = "already_running"
	case coreserver.EventStoppingOld:
		out.Kind = "stopping_old"
	case coreserver.EventStarted:
		out.Kind = "started"
	case coreserver.EventStopped:
		out.Kind = "stopped"
	case coreserver.EventStopFailed:
		out.Kind = "stop_failed"
		if ev.Err != nil {
			out.Error = ev.Err.Error()
		}
	case coreserver.EventAlreadyStopped:
		out.Kind = "already_stopped"
	default:
		panic(fmt.Sprintf("unknown coreserver.EventKind %d", ev.Kind))
	}
	return out
}

// stateID は coreserver.Status を JSON の語彙へ写す。
func stateID(s coreserver.Status) string {
	switch s {
	case coreserver.StatusRunning:
		return StateRunning
	case coreserver.StatusCrashed:
		return StateCrashed
	case coreserver.StatusStopped:
		return StateStopped
	default:
		panic(fmt.Sprintf("unknown coreserver.Status %d", s))
	}
}
