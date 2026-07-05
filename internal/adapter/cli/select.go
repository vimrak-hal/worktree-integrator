package cli

import (
	"errors"
	"os"

	"github.com/AlecAivazis/survey/v2"
	"github.com/AlecAivazis/survey/v2/terminal"

	"github.com/vimrak-hal/worktree-integrator/internal/core/git/repo"
)

const selectTitle = "Select repositories to create worktrees for"

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
	prompt := &survey.MultiSelect{Message: selectTitle, Options: names}
	if err := survey.AskOne(prompt, &selected); err != nil {
		if errors.Is(err, terminal.InterruptErr) {
			return nil, ErrInterrupted
		}
		return nil, err
	}
	return repo.RetainNamed(repos, selected), nil
}
