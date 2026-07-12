// Package render はワークフローの型付き Result をユーザー向けのテキストへ描画する。
// ユーザー向けにバイト列を整形するものはすべてここにあり、ワークフロー（app）や
// ドメイン（core）はテキストを一切持たない。ユーザー向けの文言は日本語に統一される
// （git の stderr など、エラーに含まれる技術的詳細は原文のまま）。
//
// CLI（main）は Result をここへ渡して stdout に描画し、MCP（mcpserver）は同じ関数を
// 取り込みバッファに繋いで人間向けテキストを得る — render は特定のフロントエンド
// 専用ではないため adapter/cli 配下ではなく adapter 直下に置かれている。ライブの
// 途中経過は Progress（app.Progress の実装）が同じ文言規約で描画する。
package render

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// Emit は「エラー時も部分結果を見せる」規約の単一実装点である。res が非 nil なら、
// err の有無に関わらず draw で描画してから err を返す（res が nil の早期エラーでは
// 何も描画せず err をそのまま返す）。CLI（adapter/clirun）と MCP（adapter/mcpserver）が
// この 1 箇所を共有することで、ワークフローが部分 Result を返し始めても両フロントエンドの
// 挙動が割れない。
func Emit[R any](w io.Writer, res *R, err error, draw func(io.Writer, *R)) error {
	if res != nil {
		draw(w, res)
	}
	return err
}

// JSON は v を整形済み JSON として w へ書き出す（`--json` フラグの実装）。
func JSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// PadRight は s を width 文字に左揃えする（バイトではなくルーン単位で数えるため、
// マルチバイトの値でも整列する）。
func PadRight(s string, width int) string {
	if n := utf8.RuneCountInString(s); n < width {
		return s + strings.Repeat(" ", width-n)
	}
	return s
}

// truncate は s を最大 limit ルーンに短縮し、切り詰めた場合は "…" を付加する。
func truncate(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	runes := []rune(s)
	return string(runes[:limit-1]) + "…"
}

// dash は空文字列をテーブルのプレースホルダ "-" に写す。
func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// tagged は "  [repo/server] ..." 形式の 1 行を書き出す。
func tagged(w io.Writer, tag, format string, args ...any) {
	fmt.Fprintf(w, "  [%s] %s\n", tag, fmt.Sprintf(format, args...))
}
