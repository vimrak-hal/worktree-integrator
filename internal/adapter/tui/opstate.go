package tui

import (
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

// maxEvents はイベント履歴の保持上限。古い側から捨ててメモリを一定に保つ。
const maxEvents = 100

// opState は統合操作の実行状態とイベント・状態行。opRunning 中は新しい操作を受け付けず
// （1 つずつ実行する）、完了までスピナーを回す。events はライフサイクル・作成進捗の履歴、
// note は状態行の一時メッセージ。
type opState struct {
	// opRunning 中は新しい統合操作を受け付けない（1 つずつ実行する）。
	opRunning bool
	opLabel   string
	// spin は opRunning 中だけ回す控えめなスピナー（MiniDot・colorAccent）。tick は
	// startOp で開始し、opRunning が下りたら次の TickMsg で止める（無駄な再描画を避ける）。
	spin spinner.Model
	// quitAfterOp は操作中に終了要求があったことを表す（完了を待って終了する）。
	quitAfterOp bool
	// events はライフサイクル・作成進捗のイベント履歴。左カラム下のイベントボックスへ
	// 直近 visibleEvents 件を時系列（新しいものが下）で表示する。
	events []string
	// note は状態行の一時メッセージ（直近の操作結果・警告）。イベントボックスの
	// 1 行目（退避時はツリーボックス最下行）に出る。
	note    string
	noteErr bool
}

// startOp は統合操作を 1 つずつ実行するためのガード。実行中なら弾いて note を出す。開始
// できたらスピナーの tick を開始する（opRunning の間だけ回り、完了で止まる）。
func (os *opState) startOp(label string, cmd tea.Cmd) tea.Cmd {
	if os.opRunning {
		os.note, os.noteErr = "別の操作を実行中です", true
		return nil
	}
	os.opRunning = true
	os.opLabel = label
	os.note, os.noteErr = "", false
	return tea.Batch(cmd, os.spin.Tick)
}

// finish は操作完了を反映する（summary を note に、err があれば括弧書きで併記する）。
func (os *opState) finish(summary string, err error) {
	os.opRunning = false
	os.opLabel = ""
	os.note, os.noteErr = summary, err != nil
	if err != nil {
		os.note = summary + "（" + err.Error() + "）"
	}
}

// addEvent はイベント履歴へ 1 行追記し、maxEvents を超えた古い側を捨てる。
func (os *opState) addEvent(line string) {
	os.events = append(os.events, line)
	if len(os.events) > maxEvents {
		os.events = os.events[len(os.events)-maxEvents:]
	}
}

// statusLine は状態行を返す: 実行中はスピナー付きの「実行中: …」を、そうでなければ直近の
// 一時メッセージ（note）を出す（startOp で note はクリアされるため両者は時間的に排他）。
// どちらも無ければ空文字。
func (os *opState) statusLine() string {
	switch {
	case os.opRunning:
		// MiniDot のスピナーを colorAccent で先頭に回す（実行中のみ tick を回す）。
		return os.spin.View() + " " + styNote.Render("実行中: "+os.opLabel+" …")
	case os.note != "":
		return os.noteLine()
	default:
		return ""
	}
}

// noteLine はフッターの一時メッセージ行（左ペインのボーダー内に置く）。
func (os *opState) noteLine() string {
	if os.note == "" {
		return ""
	}
	if os.noteErr {
		return styErrNote.Render(os.note)
	}
	return styNote.Render(os.note)
}

// eventLines はイベントボックスの内側（1 + visibleEvents 行）を組む: 1 行目に状態行、続けて
// 直近のイベント（新しいものが下）。不足行は renderBox が空白で埋めるためここでは詰めない。
func (os *opState) eventLines() []string {
	out := []string{os.statusLine()}
	ev := os.events
	if len(ev) > visibleEvents {
		ev = ev[len(ev)-visibleEvents:]
	}
	return append(out, ev...)
}
