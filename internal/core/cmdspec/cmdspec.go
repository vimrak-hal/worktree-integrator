// Package cmdspec は、設定されたシェルコマンドを定義する。単一のコマンド行か、
// 順番に実行されるコマンド行のリストのいずれかを表す。
//
// このツールがシェルを介して実行するすべてのもの（フックおよびサーバーの
// ライフサイクル／起動コマンドの両方）に共通するプリミティブであるため、
// いずれかのサブシステムに属するのではなく、独立した中立的なパッケージとして存在する
// （旧 infra/shellcmd。設定スキーマの語彙は infra に置かないという規約に従い、
// Phase 5 で core 配下へ移設した）。
//
// 設定ファイルでは、コマンドは文字列または文字列の配列のいずれかで記述する:
//
//	command = "npm install"                 # 1 つのコマンド
//	command = ["npm ci", "npm run build"]   # 複数、順番に実行
//
// 複数のコマンドは "&&" で連結されるため、左から右へ実行され、最初の失敗で停止する。
// シーケンス全体が単一の `sh -c` 呼び出しに渡されるため、バックグラウンドコマンドは
// 待機することなくシーケンス全体を起動する。
//
// `command = []`（空配列）や空文字列の行は、設定ミスである可能性が高く実行しても
// 何も起きないため、UnmarshalTOML の時点でエラーとして拒否する（キーが存在しない
// ケース = IsEmpty とは区別される）。
package cmdspec

import (
	"fmt"
	"strings"
)

// Commands は 1 つ以上のシェルコマンド行を表す。TOML 表現は、文字列（1 つの
// コマンド）または文字列の配列（複数、順番に実行）のタグなし共用体である。
type Commands struct {
	// lines はコマンド行を保持する。単一文字列形式の場合は長さ 1 となる。
	lines []string
}

// FromString は単一コマンドの Commands を構築する（デフォルト／テスト用の便宜関数）。
// UnmarshalTOML と異なり、空文字列も受理する（プログラムからの構築であり、設定ミスの
// 検出対象ではないため）。
func FromString(command string) Commands {
	return Commands{lines: []string{command}}
}

// UnmarshalTOML は、素の文字列（1 つのコマンド）または文字列の配列（複数、
// 順番に実行）のいずれかをデコードする。それ以外はエラーとなる。空配列
// （`command = []`）と空文字列の行は、設定ミスの可能性が高いため拒否する。
func (c *Commands) UnmarshalTOML(v any) error {
	switch t := v.(type) {
	case string:
		if t == "" {
			return fmt.Errorf("command must not be an empty string")
		}
		c.lines = []string{t}
	case []any:
		if len(t) == 0 {
			return fmt.Errorf("command array must not be empty")
		}
		lines := make([]string, 0, len(t))
		for _, e := range t {
			s, ok := e.(string)
			if !ok {
				return fmt.Errorf("command array must contain only strings, got %T", e)
			}
			if s == "" {
				return fmt.Errorf("command array must not contain empty strings")
			}
			lines = append(lines, s)
		}
		c.lines = lines
	default:
		return fmt.Errorf("command must be a string or an array of strings, got %T", v)
	}
	return nil
}

// Script は `sh -c` に渡すシェルスクリプトを返す。単一コマンドはそのまま、
// 複数コマンドは順番に実行され最初の失敗で停止するよう " && " で連結される。
func (c Commands) Script() string {
	return strings.Join(c.lines, " && ")
}

// IsEmpty は、コマンドがまったく設定されていない（キーが存在しない）かどうかを返す。
// これは明示的に空のコマンド文字列とは区別される。必須のコマンドフィールドが
// 指定されているかを検証するために使われる。
func (c Commands) IsEmpty() bool {
	return len(c.lines) == 0
}
