package tui

import (
	"hash/fnv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// スタイルは基本 16 色（ANSI）だけを使う。dev サーバーのログを見る端末は多様で
// あり、256 色やトゥルーカラーを仮定しない。
var (
	styTabActive   = lipgloss.NewStyle().Bold(true).Reverse(true).Padding(0, 1)
	styTabInactive = lipgloss.NewStyle().Faint(true).Padding(0, 1)
	styFlag        = lipgloss.NewStyle().Foreground(lipgloss.Color("6")) // シアン
	styHelp        = lipgloss.NewStyle().Faint(true)
	styNote        = lipgloss.NewStyle().Foreground(lipgloss.Color("3")) // 黄
	styErrNote     = lipgloss.NewStyle().Foreground(lipgloss.Color("1")) // 赤
	stySelected    = lipgloss.NewStyle().Reverse(true)
	styHeaderRow   = lipgloss.NewStyle().Bold(true).Underline(true)

	styLineError = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	styLineWarn  = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))

	styStateRunning = lipgloss.NewStyle().Foreground(lipgloss.Color("2")) // 緑
	styStateCrashed = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	styStateStopped = lipgloss.NewStyle().Faint(true)
)

// tagPalette はマージ表示で出所タグ（repo/server）に割り当てる色。タグ名のハッシュで
// 決まるため、同じサーバーは常に同じ色になる。
var tagPalette = []lipgloss.Style{
	lipgloss.NewStyle().Foreground(lipgloss.Color("4")), // 青
	lipgloss.NewStyle().Foreground(lipgloss.Color("2")), // 緑
	lipgloss.NewStyle().Foreground(lipgloss.Color("5")), // マゼンタ
	lipgloss.NewStyle().Foreground(lipgloss.Color("6")), // シアン
	lipgloss.NewStyle().Foreground(lipgloss.Color("3")), // 黄
}

// tagStyle は tag に決定的な色を割り当てる。
func tagStyle(tag string) lipgloss.Style {
	h := fnv.New32a()
	h.Write([]byte(tag))
	return tagPalette[h.Sum32()%uint32(len(tagPalette))]
}

// colorizeLog はログの 1 行にレベル推定の色付けをする。構造化ログではないため
// ヒューリスティック（単語の包含）であり、error / fatal / panic を赤、warn を黄に
// する。可読性の補助であって分類の保証ではない。
func colorizeLog(text string) string {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "error") || strings.Contains(lower, "fatal") ||
		strings.Contains(lower, "panic") || strings.Contains(lower, "traceback"):
		return styLineError.Render(text)
	case strings.Contains(lower, "warn"):
		return styLineWarn.Render(text)
	default:
		return text
	}
}

// pad は s を表示幅 w に左揃えする。render.PadRight（ルーン数）と違い、全角文字の
// 表示幅（East Asian Width）で数えるため、TUI のテーブル列が日本語の別名でも崩れない。
// 色付け前の素の文字列にのみ使う（エスケープシーケンスは幅として誤計上される）。
func pad(s string, w int) string {
	return runewidth.FillRight(runewidth.Truncate(s, w, "…"), w)
}

// truncDisplay は（色付けを含みうる）1 行を表示幅 w に収める。ANSI エスケープを
// 幅 0 として扱い、エスケープの途中で切らない。
func truncDisplay(s string, w int) string {
	return ansi.Truncate(s, w, "…")
}

// wrapDisplay は（色付けを含みうる）1 行を表示幅 w で折り返す。
func wrapDisplay(s string, w int) string {
	return ansi.Hardwrap(s, w, true)
}
