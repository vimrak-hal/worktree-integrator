package server

import (
	"fmt"
	"path/filepath"
	"strings"
)

// encodeLogComponent は名前をログファイル名の 1 コンポーネントへ単射に写す。
// 置換は '%'→"%25"、'/'→"%2F"、'_'→"%5F" の 3 つのみ。'%' を先にエスケープする
// ため復号は一意であり、エンコード結果は '_' を含まないので、コンポーネントを
// "__" で連結してもどこが区切りかが一意に定まる（旧実装の '/'→'-' 平坦化は
// "a/b" と "a-b" が衝突し、名前中の '_' は "__" 区切りと衝突する非単射変換だった）。
func encodeLogComponent(name string) string {
	name = strings.ReplaceAll(name, "%", "%25")
	name = strings.ReplaceAll(name, "/", "%2F")
	name = strings.ReplaceAll(name, "_", "%5F")
	return name
}

// LogPath は、あるワークツリーにおけるリポジトリのサーバーに対する決定論的な
// ログファイルパス。repo・server・worktree の各名前を単射エンコードしてから "__" で
// 連結するため、(repo, server, worktree) の組とログファイル名は 1 対 1 に対応する。
// repo と server 名は検証済みで '/' を含まないが、防御的に同じ関数を通す。
func (s *StateStore) LogPath(repo, server, worktree string) string {
	return filepath.Join(s.LogsDir(), fmt.Sprintf("%s__%s__%s.log",
		encodeLogComponent(repo), encodeLogComponent(server), encodeLogComponent(worktree)))
}

// PrevLogPath は path の 1 世代前のログ（SpawnDetached がローテートした .prev）の
// パスを返す。
func PrevLogPath(path string) string { return path + ".prev" }

// ParseLogName はログファイルのベース名を (repo, server, worktree) の組へ復号する。
// LogPath の単射エンコードの逆写像であり、worktree ライフサイクル（remove のログ
// 削除・doctor の孤児ログ検出）が「このログはどの worktree のものか」をファイル名
// だけから決定できるようにする。prev は 1 世代前のログ（.log.prev）かどうか。
// このツールの命名規則に従わないファイル名は ok=false（他人のファイルには触れない）。
func ParseLogName(base string) (repo, server, worktree string, prev bool, ok bool) {
	if rest, found := strings.CutSuffix(base, ".prev"); found {
		base = rest
		prev = true
	}
	base, found := strings.CutSuffix(base, ".log")
	if !found {
		return "", "", "", false, false
	}
	// エンコード済みコンポーネントは '_' を含まない（'_'→"%5F"）ため、"__" 区切りは
	// 一意に定まる。
	parts := strings.Split(base, "__")
	if len(parts) != 3 {
		return "", "", "", false, false
	}
	decoded := make([]string, 3)
	for i, part := range parts {
		d, dok := decodeLogComponent(part)
		if !dok {
			return "", "", "", false, false
		}
		decoded[i] = d
	}
	return decoded[0], decoded[1], decoded[2], prev, true
}

// decodeLogComponent は encodeLogComponent の逆写像。有効なエスケープは
// %25（'%'）・%2F（'/'）・%5F（'_'）の 3 つのみで、それ以外の '%' の使われ方や
// 生の '_'（"__" 区切りにのみ現れるはず）を含む入力は ok=false。
func decodeLogComponent(s string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '%':
			if i+3 > len(s) {
				return "", false
			}
			switch s[i+1 : i+3] {
			case "25":
				b.WriteByte('%')
			case "2F":
				b.WriteByte('/')
			case "5F":
				b.WriteByte('_')
			default:
				return "", false
			}
			i += 2
		case '_':
			return "", false
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String(), true
}
