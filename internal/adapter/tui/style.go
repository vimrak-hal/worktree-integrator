package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// スタイルは基本 16 色（ANSI）だけを使う。dev サーバーのログを見る端末は多様で
// あり、256 色やトゥルーカラーを仮定しない。
var (
	styFlag     = lipgloss.NewStyle().Foreground(lipgloss.Color("6")) // シアン
	styHelp     = lipgloss.NewStyle().Faint(true)
	styNote     = lipgloss.NewStyle().Foreground(lipgloss.Color("3")) // 黄
	styErrNote  = lipgloss.NewStyle().Foreground(lipgloss.Color("1")) // 赤
	stySelected = lipgloss.NewStyle().Reverse(true)

	styLineError = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	styLineWarn  = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))

	// ツリーのサーバーノードの状態マーク。稼働=緑、クラッシュ=赤、停止=faint。
	styMarkRunning = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styMarkCrashed = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	styMarkStopped = lipgloss.NewStyle().Faint(true)

	// ペインの見出し。フォーカス側は反転+太字、非フォーカス側は faint。どちらの
	// ペインを操作しているかを一目で分かるようにする。
	styPaneTitle      = lipgloss.NewStyle().Faint(true)
	styPaneTitleFocus = lipgloss.NewStyle().Reverse(true).Bold(true)
)

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

// padDisplay は（色付けを含みうる）1 行を表示幅 w に「切り詰め + 右パディング」する。
// 左ペインの色付き行を 2 ペイン結合の左カラムへ収める用途であり、truncDisplay と違い
// 不足分を空白で埋めて幅を w に固定する（ANSI エスケープは幅 0 として扱う）。
func padDisplay(s string, w int) string {
	s = ansi.Truncate(s, w, "…")
	if gap := w - lipgloss.Width(s); gap > 0 {
		s += strings.Repeat(" ", gap)
	}
	return s
}
