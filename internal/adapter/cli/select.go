package cli

import (
	"errors"
	"os"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	"github.com/vimrak-hal/worktree-integrator/internal/core/git/repo"
)

const selectTitle = "Select repositories to create worktrees for"

// selectTheme は SelectRepos のプロンプト配色。基本 16 色（ANSI）だけを使う
// 既存の見た目に馴染ませるため huh.ThemeBase() を土台にし、フォーカス中の
// タイトルだけをシアン（色 6）に寄せる（派手な色は使わない）。tui/forms.go の
// formTheme と同じ発想だが、アダプタ間の依存を作らないため cli パッケージ内に
// 独立して小さく定義する（tui からは import しない）。
func selectTheme() *huh.Theme {
	t := huh.ThemeBase()
	t.Focused.Title = t.Focused.Title.Foreground(lipgloss.Color("6"))
	return t
}

// InteractiveSelector は、stdin と stdout がともに端末である場合に SelectRepos を、
// そうでなければ nil（非対話）を返す。main が App.Selector に注入し、非 TTY
// （パイプ・リダイレクト・CI）での `create` は対話プロンプトへ進まず「--repo か
// --all を指定してください」というエラーになる（app/create の冒頭ガード）。
func InteractiveSelector() func([]repo.Repo) ([]repo.Repo, error) {
	if isTerminal(os.Stdin) && isTerminal(os.Stdout) {
		return SelectRepos
	}
	return nil
}

// isTerminal は f がキャラクタデバイス（端末）かどうかを返す。
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// ErrInterrupted は対話プロンプトが Ctrl-C で中断されたことを表す型付きエラー。
// huh は中断時に huh.ErrUserAborted を返すため、SelectRepos が errors.Is で
// これへ写像する。
// 「空選択（何も選ばず Enter）＝ 何もしない正常終了」とは区別され、main が
// exit 130 に写像する（シェルの 128+SIGINT 慣習。旧実装はキャンセルを空選択と
// 同一視して exit 0 にしていたが、意図的な仕様変更として中断を中断のまま伝える）。
var ErrInterrupted = errors.New("対話プロンプトが中断されました")

// SelectRepos は、リポジトリのインタラクティブなチェックボックスリストを表示し、
// ユーザーが選択した部分集合を、検出（ソート済み）順を維持して返す。create.Selector
// として注入され、端末 I/O を所有する CLI アダプタにこの対話処理を閉じ込める
// （core は端末 I/O を持たない）。
//
// （nil エラーを伴う）空の結果は、ユーザーが何も選択せずに確定したことを意味し、
// 呼び出し側では「何もしない」として扱われる。プロンプトの中断（Ctrl-C）は
// ErrInterrupted として返る。
func SelectRepos(repos []repo.Repo) ([]repo.Repo, error) {
	names := make([]string, len(repos))
	for i, r := range repos {
		names[i] = r.Name
	}

	var selected []string
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title(selectTitle).
				Options(huh.NewOptions(names...)...).
				Value(&selected),
		),
	).WithTheme(selectTheme())
	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return nil, ErrInterrupted
		}
		return nil, err
	}
	return repo.RetainNamed(repos, selected), nil
}
