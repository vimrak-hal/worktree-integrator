package action

import (
	"fmt"
	"strings"
)

// Name は検証済みの worktree 名（＝ブランチ名＝worktrees_dir 直下の相対パス）である。
// 生成経路は ParseName のみであり（parse-don't-validate）、Name を受け取る側は
// 名前が既に検証済みであることを型で保証される。ゼロ値の Name は空文字列を表すが、
// ParseName は空文字列を拒否するため、通常の経路では出現しない。
type Name struct{ s string }

// ParseName は raw を検証して Name を返す。Name を構築する唯一の経路である。
//
// 検証規則は git check-ref-format に準拠して緩和されている。名前は "/" でセグメントに
// 分割され、各セグメントは英数字・'.'・'_'・'-' のみを含む。空セグメント、"." と ".."
// セグメント、セグメント先頭の '-'（git がオプションとして解釈する）と '.'、".lock" で
// 終わるセグメント（git の参照ロックと衝突する）は拒否される。空白・制御文字・
// `: ? * [ \ ~ ^` および "@{" は文字の許可リストによって自動的に拒否される。
// これにより "feat_login" や "v1.2" のような名前が合法になる。
func ParseName(raw string) (Name, error) {
	if raw == "" {
		return Name{}, fmt.Errorf("worktree 名が空です")
	}
	for seg := range strings.SplitSeq(raw, "/") {
		if err := validateSegment("worktree 名", raw, seg); err != nil {
			return Name{}, err
		}
	}
	return Name{s: raw}, nil
}

// String は検証済みの名前をそのまま返す。ブランチ名としても相対パスとしても使える。
func (n Name) String() string { return n.s }

// validateRepoName は name がリポジトリ名（＝単一のパスコンポーネント）として安全か
// を検証する。ParseName と同じセグメント規則を 1 セグメント分だけ適用し、"/" を含む
// 名前は拒否する。--repo / MCP の repos で与えられる名前がパス結合に使われるため、
// トラバーサルやオプション混同を型の手前で塞ぐ。
func validateRepoName(name string) error {
	if name == "" {
		return fmt.Errorf("リポジトリ名が空です")
	}
	if strings.ContainsRune(name, '/') {
		return fmt.Errorf("リポジトリ名 %q が不正です: \"/\" は使えません", name)
	}
	return validateSegment("リポジトリ名", name, name)
}

// validateSegment は 1 つのパスセグメントを検証し、違反時には「何が違反か」を具体的に
// 伝えるエラーを返す。what はエラーメッセージの主語（"worktree 名" など）、whole は
// 元の入力全体（メッセージでの提示用）。
func validateSegment(what, whole, seg string) error {
	switch {
	case seg == "":
		return fmt.Errorf("%s %q が不正です: 空のセグメント（先頭・末尾・連続した \"/\"）は使えません", what, whole)
	case seg == "." || seg == "..":
		return fmt.Errorf("%s %q が不正です: セグメント %q はパストラバーサルになるため使えません", what, whole, seg)
	case seg[0] == '-':
		return fmt.Errorf("%s %q が不正です: セグメント先頭の '-' は git のオプションとして解釈されるため使えません", what, whole)
	case seg[0] == '.':
		return fmt.Errorf("%s %q が不正です: セグメント先頭の '.' は使えません", what, whole)
	case strings.HasSuffix(seg, ".lock"):
		return fmt.Errorf("%s %q が不正です: \".lock\" で終わるセグメントは git の参照ロックと衝突するため使えません", what, whole)
	}
	for _, c := range seg {
		if !isNameRune(c) {
			return fmt.Errorf("%s %q が不正です: 使用できない文字 %q を含みます（英数字・'.'・'_'・'-'・区切りの '/' のみ使えます）", what, whole, string(c))
		}
	}
	return nil
}

// isNameRune はセグメント内で許可される文字（英数字・'.'・'_'・'-'）かどうかを返す。
// 許可リスト方式のため、空白・制御文字・`: ? * [ \ ~ ^ @` などは自動的に拒否される。
func isNameRune(c rune) bool {
	return isAlphanumeric(c) || c == '.' || c == '_' || c == '-'
}

func isAlphanumeric(c rune) bool {
	// ASCII の英数字のみ。Unicode の英数字まで広げるとファイルシステムの正規化差
	// （macOS の NFD など）で同名衝突の面が広がるため、意図的に ASCII に限定する。
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}
