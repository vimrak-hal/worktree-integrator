//go:build linux

package proc

import (
	"errors"
	"fmt"
	"os"
	"strconv"
)

// GroupReaped は、pgid のプロセスグループについて「観測できたメンバーが 1 つ以上あり、
// その全員が状態 Z（ゾンビ）である」ときにのみ true を返す保守的な消滅判定である。
// SIGKILL 済みだが親に未回収のメンバー（ゾンビ）は kill(-pgid, 0) には生存として
// 数えられ続けるが、実行はすでに終わっているため、停止確認の観点では「消滅」とみなしてよい。
//
// この関数は kill(-pgid, 0) がグループの存在を示した文脈でのみ呼ばれる（procctl.Alive
// が groupAlive の後にのみ呼ぶ）。したがって /proc 上でメンバーを 1 つも観測できない
// 場合は「全滅」ではなく「hidepid 等で観測不能」と解釈し、false（消滅と断定しない）を
// 返す。真に全滅していれば kill(-pgid, 0) が ESRCH を返して groupAlive が偽になり、この
// 関数はそもそも呼ばれない。同様に、メンバーの stat が EPERM/EACCES 等で読めない場合も
// 「見えないが存在する可能性」があるため false を返す（ENOENT による消滅レースのみ無視）。
//
// ゾンビを消滅扱いにしても誤殺リスクは増えない: 停止経路は proc.Ident（PID + 開始時刻）
// による同一性照合で対象を特定しており、pgid 番号の一致だけでシグナルを送ることはない。
// PID 1 が最小 init のコンテナ環境では孤児のゾンビが回収されないため、この判定がないと
// グループはいつまでも生存扱いになる。
func GroupReaped(pgid int) bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		// /proc を読めない場合は消滅を確認できないので、生存扱いを保つ。
		return false
	}
	observed := false
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // /proc 直下の非 PID エントリ（"stat" など）は飛ばす。
		}
		state, pgrp, err := procStatePgrp(pid)
		if err != nil {
			if errors.Is(err, ErrGone) {
				continue // ENOENT: 走査中に消えた（レース）は消滅として無視する。
			}
			// EPERM/EACCES 等で読めない: 見えないが存在し得るため消滅と断定しない。
			return false
		}
		if pgrp != pgid {
			continue
		}
		observed = true
		if state != "Z" {
			return false // 実行中のメンバーが 1 つでもあれば消滅していない。
		}
	}
	if !observed {
		// メンバーを 1 つも観測できなかった。真の全滅なら kill が ESRCH を返して
		// ここへは来ないため、観測不能（hidepid 等）と解釈し消滅と断定しない。
		return false
	}
	// メンバーを 1 つ以上観測し、そのすべてがゾンビだった。
	return true
}

// procStatePgrp は /proc/<pid>/stat から state（プロセス状態）と pgrp（プロセスグループ
// ID）を読む。state は ')' 直後の 1 番目、pgrp は 3 番目のフィールド（statFields が comm
// 以降を切り出す）。読み取り・解析に失敗した場合は err を返す（ENOENT は statFields が
// ErrGone に正規化する）。
func procStatePgrp(pid int) (state string, pgrp int, err error) {
	fields, err := statFields(pid)
	if err != nil {
		return "", 0, err
	}
	if len(fields) < 3 {
		return "", 0, fmt.Errorf("/proc/%d/stat を解析できません: 予期しない形式です", pid)
	}
	pgrp, err = strconv.Atoi(fields[2])
	if err != nil {
		return "", 0, fmt.Errorf("/proc/%d/stat の pgrp %q を解析できません: %w", pid, fields[2], err)
	}
	return fields[0], pgrp, nil
}

// LeaderAlive は、id.Pid（プロセスグループのリーダー）が今も生きていて、我々が起動した
// プロセスと同一であることを、/proc/<pid>/stat の 1 回の読み取りで確認する定数コストの
// 高速パスである。リーダーが存在し、状態が Z（ゾンビ）でなく、開始時刻が id と一致する
// ときにのみ true を返す。リーダーが消滅・ゾンビ化・同一性不一致のいずれかのときは
// false を返し、呼び出し側（Alive）は GroupReaped による全走査のフォールバック判定へ進む。
func LeaderAlive(id Ident) bool {
	state, start, err := leaderStat(id.Pid)
	if err != nil {
		return false
	}
	return state != "Z" && SameStart(start, id.StartUnixMs)
}
