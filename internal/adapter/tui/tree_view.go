package tui

import (
	"strconv"
	"strings"
)

// tree_view.go は treeModel（左ペイン）の描画メソッド。状態は tree_model.go が持ち、
// ここはノード行の組み立て・スクロール追従・色付けに閉じる（いずれも treeModel の受信者）。

// visibleNodeLines はツリーのノード行を組み、選択行が可視域に入るようスクロールして
// area 行ぶんへ切り詰め・空白パディングして返す（フッター合成は含まない）。
func (tm *treeModel) visibleNodeLines(area int) []string {
	var nodeLines []string
	switch {
	case tm.treesErr != "":
		nodeLines = []string{styErrNote.Render("取得失敗: " + tm.treesErr)}
	case tm.trees == nil:
		nodeLines = []string{"読み込み中…"}
	case len(tm.nodes) == 0:
		nodeLines = []string{"worktree がありません（n で作成）"}
	default:
		nodeLines = make([]string, len(tm.nodes))
		for i, n := range tm.nodes {
			nodeLines[i] = tm.nodeLine(i, n)
		}
	}

	tm.adjustTreeTop(len(nodeLines), area)
	visible := nodeLines
	if tm.treeTop < len(visible) {
		visible = visible[tm.treeTop:]
	} else {
		visible = nil
	}
	if len(visible) > area {
		visible = visible[:area]
	}
	for len(visible) < area {
		visible = append(visible, "")
	}
	return visible
}

// adjustTreeTop は sel が可視域（treeTop..treeTop+viewH）に入るようスクロール位置を
// 詰める。ノード数が領域に満たなければ先頭に固定する。
func (tm *treeModel) adjustTreeTop(total, viewH int) {
	if tm.sel < tm.treeTop {
		tm.treeTop = tm.sel
	}
	if tm.sel >= tm.treeTop+viewH {
		tm.treeTop = tm.sel - viewH + 1
	}
	tm.treeTop = clamp(tm.treeTop, 0, max(0, total-viewH))
}

// nodeLine は 1 ノードを描く。選択行は反転をやめ、行頭に colorAccent の ▌ インジケータを
// 立てて本文を太字にする（非選択行は行頭 1 桁の空白で整列を保つ）。状態マークは選択・
// 非選択のいずれも状態色を保つ（インジケータ方式なのでマーク色と共存できる）。
func (tm *treeModel) nodeLine(i int, n node) string {
	sel := i == tm.sel
	// 行頭 1 桁: 選択は ▌（accent）、非選択は空白で整列。
	ind := " "
	if sel {
		ind = stySelIndicator.Render("▌")
	}

	if n.isWorktree() {
		// 展開中は ▾、折りたたみ中は ▸。折りたたみは配下サーバーが見えないため、状態を
		// 集約したマーク（例: ●2 ✗1）を見出しの右に添える。
		glyph := "▾"
		if n.collapsed {
			glyph = "▸"
		}
		name := glyph + " " + n.wt
		if n.alias != "" {
			name += " (" + n.alias + ")"
		}
		if n.broken {
			name += " (!)"
		}
		if sel {
			// 見出し本文だけ太字にし、集約マークは状態色を保つ（太字で色を潰さない）。
			name = stySelText.Render(name)
		}
		if agg := aggColored(n); n.collapsed && agg != "" {
			name += "  " + agg
		}
		return ind + name
	}

	suffix := n.repo + "/" + n.server
	if n.running && n.pid != 0 {
		suffix += " :" + strconv.Itoa(n.pid)
	}
	if sel {
		suffix = stySelText.Render(suffix)
	}
	// サーバーノードは見出しの下に 2 桁インデントして並べる（マークは状態色）。
	return ind + "  " + markColored(n) + " " + suffix
}

// aggColored は集約マークを状態色付きで組む。色は状態マークの既存定数を
// 流用: 稼働=緑・クラッシュ=赤・停止=faint。
func aggColored(n node) string {
	var parts []string
	if n.nRunning > 0 {
		parts = append(parts, styMarkRunning.Render("●"+strconv.Itoa(n.nRunning)))
	}
	if n.nCrashed > 0 {
		parts = append(parts, styMarkCrashed.Render("✗"+strconv.Itoa(n.nCrashed)))
	}
	if n.nStopped > 0 {
		parts = append(parts, styMarkStopped.Render("○"+strconv.Itoa(n.nStopped)))
	}
	return strings.Join(parts, " ")
}

// markColored は色付きのマーク記号（非選択行用）。
func markColored(n node) string {
	switch {
	case n.running:
		return styMarkRunning.Render("●")
	case n.crashed:
		return styMarkCrashed.Render("✗")
	default:
		return styMarkStopped.Render("○")
	}
}
