package tui

import (
	"strings"

	kb "github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

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

// formController は最前面の huh フォーム（作成・別名・削除確認）とそのバインド値を持つ。
// form が非 nil の間はキー入力をフォームが最優先で消費し、完了時に finishForm が値を取り
// 出して dispatch する。値は各 form* フィールドへポインタでバインドされる。フォームを開く・
// ヘルプ行を出す・ダイアログを描くのはここに閉じ、完了時の操作 dispatch（startOp を伴う
// 横断処理）はルートの finishForm が担う。
type formController struct {
	form        *huh.Form
	formKind    formKind
	formName    string
	formRepos   []string
	formAlias   string
	formConfirm bool
	// promptTarget は alias / remove フォームの対象 worktree 名（フォームを開いた
	// 時点のカーソル位置を固定して保持する）。
	promptTarget string
	repos        *app.ReposResult
}

// openCreate は worktree 作成フォーム（名前入力 + リポジトリ複数選択）を開く（reposMsg 受信時）。
func (fc *formController) openCreate(res *app.ReposResult, innerW, formH int) tea.Cmd {
	fc.repos = res
	fc.formName = ""
	fc.formRepos = nil
	fc.form = newCreateForm(res.Repos, &fc.formName, &fc.formRepos, innerW).WithHeight(formH)
	fc.formKind = formCreate
	return fc.form.Init()
}

// openAlias は別名フォームを開き、現在のラベルをプリフィルする（対象 worktree を固定して保持）。
func (fc *formController) openAlias(wt, label string, innerW, formH int) tea.Cmd {
	fc.promptTarget = wt
	fc.formAlias = label
	fc.form = newAliasForm(&fc.formAlias, innerW).WithHeight(formH)
	fc.formKind = formAlias
	return fc.form.Init()
}

// openRemove は削除確認フォームを開く（対象 worktree を固定して保持、既定は否定）。
func (fc *formController) openRemove(wt string, innerW, formH int) tea.Cmd {
	fc.promptTarget = wt
	fc.formConfirm = false
	fc.form = newRemoveForm(wt, &fc.formConfirm, innerW).WithHeight(formH)
	fc.formKind = formRemove
	return fc.form.Init()
}

// clear はフォームを畳む（form=nil・formKind=formNone）。完了・中止のいずれでも呼ぶ。
func (fc *formController) clear() {
	fc.form = nil
	fc.formKind = formNone
}

// contextBindings はフォーム表示中のヘルプ行の並びを返す（表示専用。キーの実処理は huh 側）。
// フォーカス中フィールドの型で並びを出し分け、Input・Confirm・MultiSelect で実際に効くキー
// だけを見せる。2 番目の戻り値はフォームが開いているかどうかで、false ならルートは他の文脈を見る。
func (fc *formController) contextBindings() ([]kb.Binding, bool) {
	if fc.form == nil {
		return nil, false
	}
	switch fc.form.GetFocusedField().(type) {
	case *huh.Input:
		return []kb.Binding{
			staticHelp("Tab/Enter", "次へ"),
			staticHelp("Shift+Tab", "戻る"),
			staticHelp("Esc", "中止"),
		}, true
	case *huh.Confirm:
		return []kb.Binding{
			staticHelp("←/→", "選択"),
			staticHelp("Enter", "確定"),
			staticHelp("Esc", "中止"),
		}, true
	default:
		// MultiSelect（作成先リポジトリ選択）ほか。
		return []kb.Binding{
			staticHelp("↑/↓", "移動"),
			staticHelp("space/x", "選択"),
			staticHelp("ctrl+a", "全選択"),
			staticHelp("Enter", "確定"),
			staticHelp("Esc", "中止"),
		}, true
	}
}

// formDialog はフォーム（作成・別名・削除確認）を中央フローティングダイアログの行の並びで
// 組む。角丸ボーダー（colorAccent）+ 上辺にアクセント太字の種類見出し。高さは huh フォームの
// 内容に合わせるため、フォーム描画の末尾の空行を落としてから箱に収める。
func (fc *formController) formDialog(innerW, height int) []string {
	label := "入力中"
	switch fc.formKind {
	case formCreate:
		label = "worktree 作成"
	case formAlias:
		label = "別名"
	case formRemove:
		label = "削除の確認"
	}
	content := trimTrailingBlank(strings.Split(fc.form.View(), "\n"))
	// 天井: 画面高から溢れないよう内容を切り詰める（ダイアログ + 上下ボーダーで height 未満に）。
	if cap := max(1, height-4); len(content) > cap {
		content = content[:cap]
	}
	return renderDialogBox(styDialogTitle.Render(label), content, innerW)
}

// formTheme は huh フォームの配色。基本 16 色（ANSI）だけを使う既存の見た目に
// 馴染ませるため huh.ThemeBase() を土台にし、フォーカス中のタイトルだけを styFlag
// 相当のシアン（色 6）に寄せる（派手な色は使わない）。
func formTheme() *huh.Theme {
	t := huh.ThemeBase()
	t.Focused.Title = t.Focused.Title.Foreground(colorAccent)
	return t
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
	).WithShowHelp(false).WithWidth(width).WithTheme(formTheme())
}

// newAliasForm はラベル入力の 1 枚フォームを組む純関数。現在のラベルを label の
// ポインタ経由でプリフィルし、空で確定すると削除・非空で設定になる（設定と削除の
// 呼び分けは appOps.Alias 側の契約）。空送信が削除になることは、huh の流儀に従い
// 入力欄の Description で明示する。
func newAliasForm(label *string, width int) *huh.Form {
	return huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("ラベル").
				Description("空のまま確定するとラベルを削除します").
				Value(label),
		),
	).WithShowHelp(false).WithWidth(width).WithTheme(formTheme())
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
	).WithShowHelp(false).WithWidth(width).WithTheme(formTheme())
}

// filterField は huh のフィールドのうち「フィルタ入力中か」を答えられるもの。huh
// v1.0.0 では MultiSelect / Select が実装し、Input / Confirm は実装しない。
type filterField interface{ GetFiltering() bool }

// formFiltering はフォーム最前面のフィールドがフィルタ入力中かどうかを返す。フィルタを
// 持たないフィールド（型アサーション失敗）や form が nil のときは false。updateKey が
// Esc をフォーム中止として畳むかどうかの判定に使う。
func formFiltering(f *huh.Form) bool {
	if f == nil {
		return false
	}
	if ff, ok := f.GetFocusedField().(filterField); ok {
		return ff.GetFiltering()
	}
	return false
}

// updateForm は huh フォーム表示中のメッセージをフォームへ渡し、完了・中止を捌く。
// キー入力に加え、huh がコマンド経由で送り返す内部メッセージ（次フィールドへ・確定）も
// ここを通る（Update 末尾のフックを参照）。Ctrl-C は updateKey が先に処理するため、
// ここには来ない。
func (m *model) updateForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	form, cmd := m.forms.form.Update(msg)
	if f, ok := form.(*huh.Form); ok {
		m.forms.form = f
	}
	switch m.forms.form.State {
	case huh.StateCompleted:
		_, opCmd := m.finishForm()
		m.forms.clear()
		return m, tea.Batch(cmd, opCmd)
	case huh.StateAborted:
		// フォーム中止（畳むだけで note は出さない）。今は Esc を updateKey が横取り
		// してここへ来る前にフォームを畳むため、この分岐は huh が内部都合で Aborted へ
		// 遷移した場合への防御として残す。
		m.forms.clear()
		return m, cmd
	}
	return m, cmd
}

// finishForm は完了状態のフォームからバインド値を取り出し、対応する統合操作へ
// dispatch する継ぎ目。form / formKind のクリアは呼び出し側（updateForm）が担う。
// テストは formKind と値を直接セットしてから呼び、正しいコマンドが起動されることを
// 検証できる。
func (m *model) finishForm() (tea.Model, tea.Cmd) {
	switch m.forms.formKind {
	case formCreate:
		name := strings.TrimSpace(m.forms.formName)
		if name == "" {
			m.op.note, m.op.noteErr = "worktree 名を入力してください", true
			return m, nil
		}
		if len(m.forms.formRepos) == 0 {
			m.op.note, m.op.noteErr = "リポジトリを 1 つ以上選択してください", true
			return m, nil
		}
		repos := append([]string(nil), m.forms.formRepos...)
		return m.startOp("create "+name, m.createCmd(name, repos))
	case formAlias:
		label := strings.TrimSpace(m.forms.formAlias)
		name := m.forms.promptTarget
		return m.startOp("alias "+name, m.aliasCmd(name, label))
	case formRemove:
		if m.forms.formConfirm {
			return m.startOp("remove "+m.forms.promptTarget, m.removeCmd(m.forms.promptTarget))
		}
		return m, nil
	}
	return m, nil
}
