package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
)

// doctorModel は doctor 結果のフローティングダイアログ表示。doctorMode 中は dvp（ダイアログ
// 専用のビューポート）が doctorText を描き、背後の右ペインは通常のログ表示のまま残る。
// dvp はログ（logModel.vp）とは別に持ち、doctor のスクロールがログ位置を動かさないようにする。
type doctorModel struct {
	doctorText []string
	doctorMode bool
	dvp        viewport.Model
}

// show は doctor 結果へ遷移し、専用ビューポート（dvp）を先頭から表示する。長い診断結果を
// 必ず先頭から読ませるため GotoTop する。背後の右ペインはログのまま。
func (dm *doctorModel) show(text []string, innerW, innerH int) {
	dm.doctorText = text
	dm.doctorMode = true
	dm.dvp = viewport.New(innerW, innerH)
	dm.setContent()
	dm.dvp.GotoTop()
}

// close はダイアログを閉じ、結果テキストを破棄する（q / Esc）。
func (dm *doctorModel) close() {
	dm.doctorMode = false
	dm.doctorText = nil
}

// resize は端末リサイズ時に dvp を追従させ、内容を再折り返しする。
func (dm *doctorModel) resize(innerW, innerH int) {
	dm.dvp.Width = innerW
	dm.dvp.Height = innerH
	dm.setContent()
}

// setContent は doctor ダイアログ専用ビューポート（dvp）へ診断テキストを流し込む。長い行は
// 横あふれを避けるため常にダイアログ幅で折り返す（doctor は w トグルの対象外）。
func (dm *doctorModel) setContent() {
	lines := make([]string, len(dm.doctorText))
	for i, l := range dm.doctorText {
		lines[i] = wrapDisplay(l, dm.dvp.Width)
	}
	dm.dvp.SetContent(strings.Join(lines, "\n"))
}

// doctorDialog は doctor 結果を中央の大きめフローティングダイアログの行の並びで組む。内容は
// 専用ビューポート（dvp）から取り、スクロール位置はキー操作で dvp が保持する。
func (dm *doctorModel) doctorDialog(innerW int) []string {
	content := strings.Split(dm.dvp.View(), "\n")
	return renderDialogBox(styDialogTitle.Render("doctor 結果"), content, innerW)
}
