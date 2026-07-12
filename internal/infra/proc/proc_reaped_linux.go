//go:build linux

package proc

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// GroupReaped は、pgid のプロセスグループに属す全プロセスが存在しないか、存在しても
// すべて状態 Z（ゾンビ）であるときに true を返す。SIGKILL 済みだが親に未回収の
// メンバー（ゾンビ）は kill(-pgid, 0) には生存として数えられ続けるが、実行はすでに
// 終わっているため、停止確認の観点では「消滅」とみなしてよい。
//
// ゾンビを消滅扱いにしても誤殺リスクは増えない: 停止経路は proc.Ident（PID + 開始
// 時刻）による同一性照合で対象を特定しており、pgid 番号の一致だけでシグナルを送る
// ことはない。PID 1 が最小 init のコンテナ環境では孤児のゾンビが回収されないため、
// この判定がないとグループはいつまでも生存扱いになる。
func GroupReaped(pgid int) bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		// /proc を読めない場合は消滅を確認できないので、生存扱いを保つ。
		return false
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // /proc 直下の非 PID エントリ（"stat" など）は飛ばす。
		}
		state, pgrp, ok := procStatePgrp(pid)
		if !ok {
			continue // 読み取り不能な PID はレース（消滅）として無視する。
		}
		if pgrp == pgid && state != "Z" {
			return false // 実行中のメンバーが 1 つでもあれば消滅していない。
		}
	}
	// メンバーが 1 つも無い（全消滅）か、見つかったメンバーがすべてゾンビだった。
	return true
}

// procStatePgrp は /proc/<pid>/stat から state（プロセス状態）と pgrp（プロセス
// グループ ID）を読む。comm フィールドは空白や括弧を含み得るため、位置での分割は
// 最後の ')' より後ろに対して行う（StartTime と同じ方針）。state は ')' 直後の
// 1 番目、pgrp は 3 番目のフィールド。読み取れない場合は ok=false。
func procStatePgrp(pid int) (state string, pgrp int, ok bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", 0, false
	}
	rest := string(data)
	if i := strings.LastIndexByte(rest, ')'); i >= 0 {
		rest = rest[i+1:]
	}
	fields := strings.Fields(rest)
	if len(fields) < 3 {
		return "", 0, false
	}
	pgrp, err = strconv.Atoi(fields[2])
	if err != nil {
		return "", 0, false
	}
	return fields[0], pgrp, true
}
