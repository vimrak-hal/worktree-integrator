package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// ANSI 基本 16 色のみを使う（端末テーマに追従させるため 256 色/TrueColor は使わない）。
const (
	colorError   = lipgloss.Color("1") // 赤: エラー・クラッシュ
	colorRunning = lipgloss.Color("2") // 緑: 稼働中
	colorWarn    = lipgloss.Color("3") // 黄: 警告
	colorAccent  = lipgloss.Color("6") // シアン: 強調・フォーカス
	// colorMuted は明るい黒（グレー）。ANSI 16 色の範囲内。faint(SGR 2) は端末により
	// 減光されず視覚差が出ないため、非フォーカスの枠は faint ではなくこの明示的な色で示す。
	colorMuted = lipgloss.Color("8") // 明るい黒（グレー）: 非フォーカスの枠・見出し
)

// スタイルは基本 16 色（ANSI）だけを使う。dev サーバーのログを見る端末は多様で
// あり、256 色やトゥルーカラーを仮定しない。背景色は端末テーマに追従させるため
// 使わず、前景色・faint・bold だけで強調を表現する。
var (
	styFlag    = lipgloss.NewStyle().Foreground(colorAccent)
	styHelp    = lipgloss.NewStyle().Faint(true)
	styNote    = lipgloss.NewStyle().Foreground(colorWarn)
	styErrNote = lipgloss.NewStyle().Foreground(colorError)

	styLineError = lipgloss.NewStyle().Foreground(colorError)
	styLineWarn  = lipgloss.NewStyle().Foreground(colorWarn)

	// ツリーのサーバーノードの状態マーク。稼働=緑、クラッシュ=赤、停止=faint。
	styMarkRunning = lipgloss.NewStyle().Foreground(colorRunning)
	styMarkCrashed = lipgloss.NewStyle().Foreground(colorError)
	styMarkStopped = lipgloss.NewStyle().Faint(true)

	// ペインの角丸ボーダー。フォーカス側は colorAccent、非フォーカス側は colorMuted。
	// どちらのペインを操作しているかをボーダー色で示す（反転は使わない）。非フォーカスは
	// faint(SGR 2) だと減光しない端末で区別が見えないため、明示的な色（グレー）で描く。
	styBorder      = lipgloss.NewStyle().Foreground(colorMuted)
	styBorderFocus = lipgloss.NewStyle().Foreground(colorAccent)

	// ペイン見出し（上辺のボーダーへ埋め込む文字列）。フォーカス側は colorAccent+太字、
	// 非フォーカス側は colorMuted。ボーダー色と揃えてフォーカスを一目で分かるようにする
	// （非フォーカスは faint ではなく明示的な色。二重にすると余計に環境差が出る）。
	styPaneTitle      = lipgloss.NewStyle().Foreground(colorMuted)
	styPaneTitleFocus = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)

	// ツリーの選択行。反転はやめ、行頭に colorAccent の ▌ インジケータを立て、行本文は
	// 太字にする（非選択行は行頭 1 桁の空白で整列を保つ）。
	stySelIndicator = lipgloss.NewStyle().Foreground(colorAccent)
	stySelText      = lipgloss.NewStyle().Bold(true)

	// ログ見出しのフラグを表すピル（バッジ）。背景色は使わず文字色のみ: 追従停止=黄、
	// 前世代/フィルタ=シアン、読取失敗=赤。
	styPillWarn   = lipgloss.NewStyle().Foreground(colorWarn)
	styPillAccent = lipgloss.NewStyle().Foreground(colorAccent)
	styPillError  = lipgloss.NewStyle().Foreground(colorError)
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
