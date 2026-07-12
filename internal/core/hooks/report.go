package hooks

import "fmt"

// Report は 1 つのフック結果の表示・シリアライズ用の表現である。ドメインの Outcome
// （Status は列挙）を、JSON スキーマとして表現可能な文字列の語彙へ写したもので、
// CLI のテキスト描画（adapter/render）・`--json`・MCP の structuredContent がすべて
// この型から派生する。フック結果の表示語彙は、フックを所有する core/hooks が持つ:
// create の各タイミングと enter の after が同じ語彙で報告する。
type Report struct {
	// Timing は "before" | "after_worktree" | "after"。
	Timing string `json:"timing"`
	Name   string `json:"name"`
	// Status は "succeeded" | "warned" | "failed"。
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// Report.Status の語彙。JSON の外部契約であり不変。
const (
	ReportSucceeded = "succeeded"
	ReportWarned    = "warned"
	ReportFailed    = "failed"
)

// Reports は 1 タイミング分のフック結果を表示・シリアライズ用の Report へ写す。
func Reports(timing string, outcomes []Outcome) []Report {
	out := make([]Report, 0, len(outcomes))
	for _, o := range outcomes {
		out = append(out, Report{
			Timing: timing,
			Name:   o.Name,
			Status: reportStatus(o.Status),
			Detail: o.Detail,
		})
	}
	return out
}

// reportStatus は Status を JSON の語彙へ写す。封印された列挙のため、未知の値は
// バグでありパニックさせる。
func reportStatus(s Status) string {
	switch s {
	case StatusSucceeded:
		return ReportSucceeded
	case StatusWarned:
		return ReportWarned
	case StatusFailed:
		return ReportFailed
	default:
		panic(fmt.Sprintf("unknown hooks.Status %d", s))
	}
}
