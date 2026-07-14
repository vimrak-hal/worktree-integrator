package action

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
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

// validateBase は base ブランチ指定を「インジェクション防御に必要な最小限」でのみ検証
// する。base は最終的に git fetch の位置引数（ブランチ名）としてリモートへ渡るため、ここ
// での役目は先頭 '-' のオプション化（"--upload-pack=<cmd>" 等）と制御文字・空白の注入を
// 型の手前で塞ぐことに限る。文字種の許可リストは敷かない: '@'・'+'・'#'・':' などは git
// として合法なブランチ名（renovate が生成する "renovate/@types-node" 等）に現れ、不正な
// ref は git 自身が自然にエラーにするため、ここで文字種を絞ると main で通っていた正当な
// 名前まで巻き込んで回帰する。base は "/" でセグメントに分割し、各セグメントの先頭 '-'・
// 制御文字・空白と、空セグメント（"a//b" や先頭・末尾の "/"）を拒否する。リテラル "auto"
// （config.DefaultBase。リモートの symbolic-ref → main → master で自動解決するセンチネル）
// は常に許可する。空文字は拒否する（呼び出し側は「未指定」を空文字で表すため、検証にかける
// 前に自分で除外する）。
func validateBase(base string) error {
	if base == config.DefaultBase {
		return nil
	}
	if base == "" {
		return fmt.Errorf("base が空です")
	}
	for seg := range strings.SplitSeq(base, "/") {
		if seg == "" {
			return fmt.Errorf("base %q が不正です: 空のセグメント（先頭・末尾・連続した \"/\"）は使えません", base)
		}
		if err := validateRefArg("base", base, seg); err != nil {
			return err
		}
	}
	return nil
}

// validateRemote は remote 指定を「インジェクション防御に必要な最小限」でのみ検証する。
// remote は git fetch の位置引数としてそのまま渡るため、先頭 '-' のオプション化・制御文字・
// 空白の混入を塞ぐことだけが役目である。"/" や ":" は URL 形式のリモート指定
// （"https://example.com/repo.git" や "user@host:path"）で git として合法なため許可する
// （文字種は絞らない — 単一トークンとして扱い、"/" でのセグメント分割はしない）。空文字は
// 拒否する。
func validateRemote(remote string) error {
	if remote == "" {
		return fmt.Errorf("remote が空です")
	}
	return validateRefArg("remote", remote, remote)
}

// validateRefArg は git の位置引数（ブランチ名・リモート名）へ渡る 1 トークンを、
// インジェクション防御に必要な最小限で検証する: 先頭 '-'（git のオプション化）と、制御
// 文字（改行含む）・空白の混入を拒否する。文字種そのものは制限しない。呼び出し側は空文字
// でないトークンを渡す（token[0] の参照が安全であることは呼び出し側が保証する）。what は
// エラーメッセージの主語、whole は元の入力全体（提示用）、token は検証対象。
func validateRefArg(what, whole, token string) error {
	if token[0] == '-' {
		return fmt.Errorf("%s %q が不正です: 先頭の '-' は git のオプション（例: --upload-pack=<cmd>）として解釈されるため使えません", what, whole)
	}
	for _, c := range token {
		if unicode.IsControl(c) || unicode.IsSpace(c) {
			return fmt.Errorf("%s %q が不正です: 制御文字・空白文字は使えません", what, whole)
		}
	}
	return nil
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
