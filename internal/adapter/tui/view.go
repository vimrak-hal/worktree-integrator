package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/vimrak-hal/worktree-integrator/internal/app/server"
)

// View は現在のモデルを 1 画面に描画する。レイアウトは固定 4 行のクローム
// （ヘッダー: タブ + コンテキストバー、フッター: メッセージ + キーヘルプ）と本文で、
// 本文の高さはビューポートと同じ height-4 に揃える。
func (m *model) View() string {
	if !m.ready {
		return "起動中…"
	}
	var context string
	var body []string
	switch m.view {
	case viewLogs:
		context = m.logsBar()
		body = []string{m.vp.View()}
	case viewStatus:
		context = styFlag.Render("サーバー状態（2 秒ごとに更新）")
		body = m.statusBody()
	case viewTrees:
		context = styFlag.Render("worktree 一覧（Enter で switch）")
		body = m.treesBody()
	}

	bodyHeight := max(1, m.height-4)
	body = fitHeight(body, bodyHeight)

	var b strings.Builder
	b.WriteString(m.tabs())
	b.WriteString("\n")
	b.WriteString(truncLine(context, m.width))
	b.WriteString("\n")
	b.WriteString(strings.Join(body, "\n"))
	b.WriteString("\n")
	b.WriteString(truncLine(m.noteLine(), m.width))
	b.WriteString("\n")
	b.WriteString(truncLine(styHelp.Render(m.helpLine()), m.width))
	return b.String()
}

// tabs はビュー切り替えのタブ行。
func (m *model) tabs() string {
	names := []string{"1:ログ", "2:ステータス", "3:worktree"}
	parts := make([]string, len(names))
	for i, name := range names {
		if viewID(i) == m.view {
			parts[i] = styTabActive.Render(name)
		} else {
			parts[i] = styTabInactive.Render(name)
		}
	}
	return truncLine(strings.Join(parts, " "), m.width)
}

// logsBar はログビューのコンテキストバー: 対象の巡回リストと、モードのフラグ
// （追従・前世代・worktree 絞り込み・フィルタ）。
func (m *model) logsBar() string {
	var parts []string
	sel := func(label string, active bool) string {
		if active {
			return stySelected.Render(" " + label + " ")
		}
		return " " + label + " "
	}
	parts = append(parts, sel("すべて", m.selKey == ""))
	for _, t := range m.targets {
		label := t.key()
		if t.missing {
			label += "(なし)"
		} else if t.readErr != "" {
			label += "(!)"
		}
		parts = append(parts, sel(label, m.selKey == t.key()))
	}

	var flags []string
	if m.follow {
		flags = append(flags, "追従")
	}
	if m.prev {
		flags = append(flags, "前世代(.prev)")
	}
	if m.scope != "" {
		flags = append(flags, "worktree:"+m.scope)
	}
	if m.filtering {
		flags = append(flags, m.input.View())
	} else if m.filter != "" {
		flags = append(flags, "/"+m.filter)
	}
	bar := strings.Join(parts, "|")
	if len(flags) > 0 {
		bar += "  " + styFlag.Render(strings.Join(flags, " "))
	}
	return bar
}

// statusBody はステータステーブルの行を組み立てる（選択行は反転）。
func (m *model) statusBody() []string {
	if m.status == nil {
		return []string{"読み込み中…"}
	}
	if len(m.status.Rows) == 0 {
		if m.status.NoServerConfig {
			return []string{"サーバー設定がありません（[servers.*] を設定してください）"}
		}
		return []string{"対象のサーバーがありません"}
	}
	rows := []string{styHeaderRow.Render(statusLine("REPO", "SERVER", "WORKTREE", "ALIAS", "PID", "状態"))}
	for i, r := range m.status.Rows {
		pid := "-"
		if r.Pid != 0 {
			pid = strconv.Itoa(r.Pid)
		}
		text := statusLine(r.Repo, r.Server, orDash(r.Worktree), orDash(r.Alias), pid, "")
		state := stateCell(r.State)
		if i == m.statusSel {
			// 反転は状態の色と競合するため、選択行は行全体を反転して状態はラベルのみ。
			rows = append(rows, stySelected.Render(text+statePlain(r.State)))
		} else {
			rows = append(rows, text+state)
		}
	}
	rows = append(rows, m.commonWarnings(m.status.LegacyBackup, m.status.UnknownRepos)...)
	return rows
}

// statusLine は列を表示幅で整形する（状態列は色付けのため呼び出し側が連結する）。
func statusLine(repo, srv, wt, alias, pid, state string) string {
	return pad(repo, 16) + pad(srv, 12) + pad(wt, 16) + pad(alias, 20) + pad(pid, 8) + state
}

// stateCell / statePlain はサーバー状態のラベル（色付き / 無色）。語彙は
// render.stateLabel と揃える。閉じた語彙のため、未知の値はバグでありパニックさせる。
func statePlain(state string) string {
	switch state {
	case server.StateRunning:
		return "稼働中 ✓"
	case server.StateCrashed:
		return "クラッシュ ✗"
	case server.StateStopped:
		return "停止 -"
	default:
		panic(fmt.Sprintf("unknown server state %q", state))
	}
}

func stateCell(state string) string {
	label := statePlain(state)
	switch state {
	case server.StateRunning:
		return styStateRunning.Render(label)
	case server.StateCrashed:
		return styStateCrashed.Render(label)
	default:
		return styStateStopped.Render(label)
	}
}

// treesBody は worktree 一覧と、直近のサーバーイベント履歴を組み立てる。
func (m *model) treesBody() []string {
	var rows []string
	switch {
	case m.treesErr != "":
		rows = append(rows, styErrNote.Render("worktree 一覧の取得に失敗: "+m.treesErr))
	case m.trees == nil:
		rows = append(rows, "読み込み中…")
	case len(m.trees.Worktrees) == 0:
		rows = append(rows, "worktree がありません（`wt <name>` で作成できます）")
	default:
		for i, wt := range m.trees.Worktrees {
			name := wt.Name
			if wt.Alias != "" {
				name += " (" + wt.Alias + ")"
			}
			if wt.Broken {
				name += " (!)"
			}
			var servers []string
			for _, s := range wt.Servers {
				servers = append(servers, fmt.Sprintf("%s/%s(pid %d)", s.Repo, s.Server, s.Pid))
			}
			detail := fmt.Sprintf("repos:%d", len(wt.Repos))
			if len(servers) > 0 {
				detail += "  稼働: " + strings.Join(servers, ", ")
			}
			text := pad(name, 34) + detail
			if i == m.treeSel {
				rows = append(rows, stySelected.Render(text))
			} else {
				rows = append(rows, text)
			}
		}
	}

	rows = append(rows, "")
	if m.opRunning {
		rows = append(rows, styNote.Render("実行中: "+m.opLabel+" …"))
	}
	if len(m.events) > 0 {
		rows = append(rows, styHelp.Render("── イベント ──"))
		events := m.events
		if len(events) > 8 {
			events = events[len(events)-8:]
		}
		rows = append(rows, events...)
	}
	return rows
}

// commonWarnings は各 Result 共通の警告（render.warnings と同じ語彙）。
func (m *model) commonWarnings(legacyBackup string, unknownRepos []string) []string {
	var rows []string
	if legacyBackup != "" {
		rows = append(rows, styNote.Render(
			"旧形式の状態ファイルを "+legacyBackup+" へ退避しました。以前から稼働中のサーバーは手動で停止してください。"))
	}
	for _, repo := range unknownRepos {
		rows = append(rows, styNote.Render(fmt.Sprintf("[%s] サーバー設定がありません（[servers.%s]）", repo, repo)))
	}
	return rows
}

// noteLine はフッターの一時メッセージ行。
func (m *model) noteLine() string {
	if m.note == "" {
		return ""
	}
	if m.noteErr {
		return styErrNote.Render(m.note)
	}
	return styNote.Render(m.note)
}

// helpLine は現在のビューのキーヘルプ。
func (m *model) helpLine() string {
	switch m.view {
	case viewLogs:
		return "←/→ 対象切替  f 追従  / フィルタ  p 前世代  w 折り返し  j/k/g/G スクロール  Esc 解除  Tab/1-3 ビュー  q 終了"
	case viewStatus:
		return "j/k 選択  Enter ログへ  Tab/1-3 ビュー  q 終了"
	case viewTrees:
		return "j/k 選択  Enter/s switch  r 再起動switch  x stop  l ログ  R 更新  Tab/1-3 ビュー  q 終了"
	}
	return ""
}

// fitHeight は本文の行数をちょうど h に揃える（不足は空行、超過は選択が見える範囲に
// 収まらない末尾を切る）。フッターの位置を安定させるための調整である。
func fitHeight(lines []string, h int) []string {
	// 複数行のセル（ビューポートの View など）を行単位に展開する。
	var flat []string
	for _, l := range lines {
		flat = append(flat, strings.Split(l, "\n")...)
	}
	if len(flat) > h {
		flat = flat[:h]
	}
	for len(flat) < h {
		flat = append(flat, "")
	}
	return flat
}

// truncLine は 1 行を表示幅 w に収める。
func truncLine(s string, w int) string {
	if w <= 0 {
		return s
	}
	return truncDisplay(s, w)
}

// orDash は空文字列をテーブルのプレースホルダ "-" に写す。
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
