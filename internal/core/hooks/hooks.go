// Package hooks はユーザーが設定可能なライフサイクルフックを実行する。これは
// ワークフローの決まったタイミングでツールが実行する外部シェルコマンドで、
// タイミングごとにグループ化されている。
//
//   - before        — リポジトリの処理を開始する前に一度だけ実行する。
//   - after_worktree — 新しく作成した worktree ごとに、その worktree 内で一度ずつ実行する。
//   - after         — すべてのリポジトリを処理し終えた後に一度だけ実行する。
//
// 同じタイミングに宣言されたフックはすべて並列に実行される。コマンドは
// `sh -c` を介して実行されるため、文字列全体がシステムのシェルによって解釈される。
//
// フックはすべてフォアグラウンド実行である。旧 background（デタッチして起動する）は
// スキーマ v2 で廃止された: 常駐プロセスの起動・監視・停止は core/server が「現実と
// 照合する状態機械」として担う責務であり、フックの中に劣化版の同じ仕組みを二重に
// 持つ理由が無いためである（config.LoadFrom が background キーを検出すると
// [repos.<repo>.servers] への移行を促すエラーで案内する）。
package hooks

import (
	"errors"
	"fmt"

	"github.com/vimrak-hal/worktree-integrator/internal/core/cmdspec"
)

// Config は設定ファイルに宣言されたすべてのフックを、タイミングごとに
// グループ化したもの。各グループは省略可能で、デフォルトは空。
type Config struct {
	Before        []Hook `toml:"before"`
	AfterWorktree []Hook `toml:"after_worktree"`
	After         []Hook `toml:"after"`
}

// IsEmpty はフックが一切設定されていないかどうかを返す。
func (c Config) IsEmpty() bool {
	return len(c.Before) == 0 && len(c.AfterWorktree) == 0 && len(c.After) == 0
}

// Validate は、TOML デコーダ自身がチェックしない必須フィールドを強制する:
// すべてのフックには name と command が必要である。所有パッケージ（hooks）自身が
// 検証を持つことで、config パッケージは「デコード → 各 Validate 呼び出し」の薄い層に
// なる。
func (c Config) Validate() error {
	check := func(timing string, list []Hook) error {
		for _, h := range list {
			if h.Name == "" {
				return fmt.Errorf("%s フックに `name` がありません", timing)
			}
			if h.Command.IsEmpty() {
				return fmt.Errorf("フック %q に `command` がありません", h.Name)
			}
		}
		return nil
	}
	if err := check("before", c.Before); err != nil {
		return err
	}
	if err := check("after_worktree", c.AfterWorktree); err != nil {
		return err
	}
	return check("after", c.After)
}

// Hook は設定された 1 つのフック。シェルコマンド（または一連のコマンド）と、
// その実行方法を保持する。
type Hook struct {
	// Name は進捗表示やサマリ出力に表示される短いラベル。
	Name string `toml:"name"`
	// Command は `sh -c` で解釈されるコマンドライン（複数可）。
	Command cmdspec.Commands `toml:"command"`
	// AllowFailure は非ゼロ終了を失敗ではなく警告として報告する。
	AllowFailure bool `toml:"allow_failure"`
	// Workdir は明示的な作業ディレクトリ。空の場合、after_worktree フックは
	// 新しく作成された worktree 内で実行され、それ以外のタイミングは
	// ツールのカレントディレクトリを引き継ぐ。
	Workdir string `toml:"workdir"`
	// TimeoutSecs はこのフックに許される最大実行時間（秒）。0（省略時）は無制限。
	// 超過すると ctx ベース（context.WithTimeout）で強制終了され、AllowFailure に
	// 従って失敗または警告として報告される。
	TimeoutSecs uint64 `toml:"timeout_secs"`
}

// Status はフックがどのように終了したかを表す。
type Status int

const (
	// StatusSucceeded: フックが正常終了した。
	StatusSucceeded Status = iota
	// StatusWarned: フックは失敗したが allow_failure が設定されていた。
	StatusWarned
	// StatusFailed: フックが失敗し、実行全体を失敗とみなすべき。
	StatusFailed
)

// Outcome は 1 つのフックを実行した結果。
type Outcome struct {
	Name   string
	Status Status
	Detail string
}

// IsFatal はこの結果によって実行全体を失敗とすべきかどうかを返す。
func (o Outcome) IsFatal() bool { return o.Status == StatusFailed }

func succeeded(name string) Outcome {
	return Outcome{Name: name, Status: StatusSucceeded}
}

// failed は失敗の結果を構築する。フックが allow_failure を選択している場合は、
// 致命的でない StatusWarned に格下げする。
func failed(name, detail string, allowFailure bool) Outcome {
	status := StatusFailed
	if allowFailure {
		status = StatusWarned
	}
	return Outcome{Name: name, Status: status, Detail: detail}
}

// ErrFailed は 1 つ以上のフックが致命的に失敗したことを表す番兵エラー。フックを
// 実行するワークフロー（create の after / after_worktree、enter の after）はこれを
// 返すため、呼び出し元は errors.Is(err, hooks.ErrFailed) でフック失敗を判別できる
// （エラーメッセージ文字列への依存を避ける）。
var ErrFailed = errors.New("1 つ以上のフックが失敗しました")

// AnyFatal は、いずれかの結果によって実行全体を失敗とすべきかどうかを返す。
// これはどの結果を致命的とみなすかというドメイン上の判断であり、結果の整形
// （adapter/render が担う）とは独立している。
func AnyFatal(outcomes []Outcome) bool {
	for _, o := range outcomes {
		if o.IsFatal() {
			return true
		}
	}
	return false
}
