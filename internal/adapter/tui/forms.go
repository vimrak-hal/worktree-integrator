package tui

import (
	"strings"

	kb "github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	"github.com/vimrak-hal/worktree-integrator/internal/app"
)

// formKind は最前面に開いている huh フォームの種類。formNone 以外のとき form は
// 非 nil で、キー入力はフォームが最優先で消費し、完了・中止は finishForm で捌く。
type formKind int

const (
	formNone formKind = iota
	formCreate
	formAlias
	formRemove
)

// formTheme は huh フォームの配色。基本 16 色（ANSI）だけを使う既存の見た目に
// 馴染ませるため huh.ThemeBase() を土台にし、フォーカス中のタイトルだけを styFlag
// 相当のシアン（色 6）に寄せる（派手な色は使わない）。
func formTheme() *huh.Theme {
	t := huh.ThemeBase()
	t.Focused.Title = t.Focused.Title.Foreground(lipgloss.Color("6"))
	return t
}

// formKeyMap は huh の既定キーマップに Esc での中止を足したもの。既定では中止は
// Ctrl-C のみだが、TUI の他のモーダル（フィルタ・doctor 結果）と同じく Esc を中止に
// 揃える。Ctrl-C は updateKey が先に握って常時 Quit のためフォームには届かない。
func formKeyMap() *huh.KeyMap {
	km := huh.NewDefaultKeyMap()
	km.Quit = kb.NewBinding(kb.WithKeys("esc", "ctrl+c"))
	return km
}

// newCreateForm は worktree 作成の 1 枚フォーム（名前入力 + 作成先リポジトリの
// 複数選択）を組む純関数。値は name / sel のポインタへバインドし、リポジトリは
// 全選択済みで開く。副作用を持たないためテストから直接検証できる。
func newCreateForm(repos []app.RepoInfo, name *string, sel *[]string, width int) *huh.Form {
	opts := make([]huh.Option[string], len(repos))
	for i, r := range repos {
		opts[i] = huh.NewOption(r.Name, r.Name).Selected(true)
	}
	return huh.NewForm(
		huh.NewGroup(
			huh.NewInput().Title("worktree 名").Value(name),
			huh.NewMultiSelect[string]().Title("作成先リポジトリ").Options(opts...).Value(sel),
		),
	).WithShowHelp(false).WithWidth(width).WithTheme(formTheme()).WithKeyMap(formKeyMap())
}

// newAliasForm は別名入力の 1 枚フォームを組む純関数。現在別名を alias のポインタ
// 経由でプリフィルし、空で確定すると削除・非空で設定になる（契約は aliasCmd 側）。
func newAliasForm(alias *string, width int) *huh.Form {
	return huh.NewForm(
		huh.NewGroup(
			huh.NewInput().Title("別名（空で削除）").Value(alias),
		),
	).WithShowHelp(false).WithWidth(width).WithTheme(formTheme()).WithKeyMap(formKeyMap())
}

// newRemoveForm は削除確認の 1 枚フォームを組む純関数。確定値は confirm のポインタへ
// バインドし、true のときだけ removeCmd を起動する（既定は false）。
func newRemoveForm(target string, confirm *bool, width int) *huh.Form {
	return huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("worktree \"" + target + "\" を削除しますか？").
				Affirmative("削除する").
				Negative("やめる").
				Value(confirm),
		),
	).WithShowHelp(false).WithWidth(width).WithTheme(formTheme()).WithKeyMap(formKeyMap())
}

// updateForm は huh フォーム表示中のメッセージをフォームへ渡し、完了・中止を捌く。
// キー入力に加え、huh がコマンド経由で送り返す内部メッセージ（次フィールドへ・確定）も
// ここを通る（Update 末尾のフックを参照）。Ctrl-C は updateKey が先に処理するため、
// ここには来ない。
func (m *model) updateForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	form, cmd := m.form.Update(msg)
	if f, ok := form.(*huh.Form); ok {
		m.form = f
	}
	switch m.form.State {
	case huh.StateCompleted:
		_, opCmd := m.finishForm()
		m.form = nil
		m.formKind = formNone
		return m, tea.Batch(cmd, opCmd)
	case huh.StateAborted:
		// Esc による中止。note は出さずにフォームを畳むだけ。
		m.form = nil
		m.formKind = formNone
		return m, cmd
	}
	return m, cmd
}

// finishForm は完了状態のフォームからバインド値を取り出し、対応する統合操作へ
// dispatch する継ぎ目。form / formKind のクリアは呼び出し側（updateForm）が担う。
// テストは formKind と値を直接セットしてから呼び、正しいコマンドが起動されることを
// 検証できる。
func (m *model) finishForm() (tea.Model, tea.Cmd) {
	switch m.formKind {
	case formCreate:
		name := strings.TrimSpace(m.formName)
		if name == "" {
			m.note, m.noteErr = "worktree 名を入力してください", true
			return m, nil
		}
		if len(m.formRepos) == 0 {
			m.note, m.noteErr = "リポジトリを 1 つ以上選択してください", true
			return m, nil
		}
		repos := append([]string(nil), m.formRepos...)
		return m.startOp("create "+name, m.createCmd(name, repos))
	case formAlias:
		label := strings.TrimSpace(m.formAlias)
		name := m.promptTarget
		return m.startOp("alias "+name, m.aliasCmd(name, label))
	case formRemove:
		if m.formConfirm {
			return m.startOp("remove "+m.promptTarget, m.removeCmd(m.promptTarget))
		}
		return m, nil
	}
	return m, nil
}
