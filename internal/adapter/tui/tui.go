// Package tui は端末上の対話 UI（`wt ui`）である。lazygit 風の 1 画面 2 ペインで、
// 左ペインに worktree → サーバーのツリー（選択・操作）、右ペインに選択サーバーの
// ログを表示する。ツリーからは server switch / stop を実行できる（全ログのマージ
// 表示は廃止した）。
//
// TUI は既存の CLI コマンドを置き換えない。各操作は同名の CLI コマンド（wt server
// switch / stop / logs）と同じワークフロー（App の型付きメソッド）を呼ぶ追加の
// フロントエンドであり、同じ操作は従来どおり CLI からも実行できる。
//
// TUI は CLI 専用の実行モードであり、action の語彙には存在しない。`server logs -f`
// （FollowLogs）と同じ理由で MCP からは型レベルで到達不能である: stdio を JSON-RPC が
// 占有する MCP の下で全画面 UI が端末を奪う事故は構造的に起こらない。
//
// 一方で、MCP サーバーや別の CLI プロセスが並行して状態を変更すること（LLM
// エージェントによる server switch など）は前提として設計されている:
//
//   - 選択中サーバーのログパス・サーバー状態・worktree 一覧は定期的に再解決される
//     （resolveCmd / treesCmd）。外部で switch が起きればログ・状態・ツリーは自動的に
//     新しい現実へ追従する。
//   - 読み取りはワークフローと同じ短命の状態ファイルロックを通り、TUI 発の操作は
//     同じ repo 操作ロックを通るため、並行操作とはリポジトリ単位で直列化される
//     （TUI だけの特別な経路は無い）。
//   - 設定ファイルは MCP サーバーと同様に定期的に再読み込みされ、編集は TUI の
//     再起動なしで反映される。
package tui

import (
	"context"
	"errors"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/statedir"
)

// Run は TUI を起動し、終了（q / Ctrl-C / ctx のキャンセル）まで端末を専有する。
// cfg / root は main が解決済みのものを受け取るが、以後の設定はティックごとに
// 読み直される（cfg は初期値であり、読めなくなったときのフォールバックでもある）。
func Run(ctx context.Context, cfg *config.File, root statedir.Root) error {
	if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
		return errors.New("ui には端末（TTY）が必要です")
	}

	fw := &forwarder{}
	m := newModel(ctx, cfg, root, fw)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithContext(ctx))
	// ワークフローの goroutine からイベントを送り返すため、プログラムの参照を
	// 転送先に渡す（ユーザー操作でワークフローが動き出す前に必ず設定される）。
	fw.p = p

	if _, err := p.Run(); err != nil {
		// シグナル（Ctrl-C / SIGTERM）による ctx のキャンセルはエラーではなく通常の
		// 終わり方であり、main の終了コード規約（130）に乗せるため ctx のエラーを返す。
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	return nil
}

// isTerminal は f がキャラクタデバイス（端末）かどうかを返す（cli.isTerminal と
// 同じ判定。パッケージ間の依存を作らないため重複させている）。
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
