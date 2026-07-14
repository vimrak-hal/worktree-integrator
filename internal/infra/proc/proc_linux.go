//go:build linux

package proc

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"
)

// userHZ は /proc/<pid>/stat の starttime フィールドの単位（クロックティック／秒）。
// カーネルはアーキテクチャによらず USER_HZ=100 でユーザー空間へ値を書き出すため、
// 定数として扱える（sysconf(_SC_CLK_TCK) も同じ 100 を返す）。
const userHZ = 100

// statFields は /proc/<pid>/stat を読み、comm フィールド（空白や括弧を含み得る）より
// 後ろのフィールド列を返す。位置での分割は最後の ')' の後ろに対して行うため、comm の
// 内容に依らず state（0 番目）・pgrp（2 番目）・starttime（19 番目）などを添字で参照
// できる。プロセスが存在しない場合（ENOENT）は ErrGone に正規化して返す。
func statFields(pid int) ([]string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("pid %d: %w", pid, ErrGone)
		}
		return nil, fmt.Errorf("/proc/%d/stat を読み取れません: %w", pid, err)
	}
	rest := string(data)
	if i := strings.LastIndexByte(rest, ')'); i >= 0 {
		rest = rest[i+1:]
	}
	return strings.Fields(rest), nil
}

// startFromFields は statFields が返したフィールド列から starttime（')' 以降の 20 番目）を
// 取り出し、/proc/stat の btime（ブート時刻）と合わせて絶対時刻へ変換する。
func startFromFields(pid int, fields []string) (time.Time, error) {
	if len(fields) < 20 {
		return time.Time{}, fmt.Errorf("/proc/%d/stat を解析できません: 予期しない形式です", pid)
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("/proc/%d/stat の starttime %q を解析できません: %w", pid, fields[19], err)
	}
	boot, err := bootTime()
	if err != nil {
		return time.Time{}, err
	}
	return boot.Add(time.Duration(ticks) * (time.Second / userHZ)), nil
}

// StartTime は pid の開始時刻を /proc/<pid>/stat の starttime（22 番目のフィールド、
// ブートからのクロックティック）と /proc/stat の btime（ブート時刻、エポック秒）から
// 合成する。プロセスが存在しない場合は ErrGone を返す。
func StartTime(pid int) (time.Time, error) {
	fields, err := statFields(pid)
	if err != nil {
		return time.Time{}, err
	}
	return startFromFields(pid, fields)
}

// leaderStat は /proc/<pid>/stat を 1 回だけ読み、プロセスの状態（state）と開始時刻を
// まとめて返す。Alive の定数コストな高速パス（LeaderAlive）が、リーダーの生存・ゾンビ
// 判定・同一性照合に必要な情報を 1 度の読み取りで得るためのヘルパー。プロセスが存在
// しない場合は ErrGone を返す。
func leaderStat(pid int) (state string, start time.Time, err error) {
	fields, err := statFields(pid)
	if err != nil {
		return "", time.Time{}, err
	}
	start, err = startFromFields(pid, fields)
	if err != nil {
		return "", time.Time{}, err
	}
	// state は ')' 直後の 1 番目のフィールド。startFromFields が len>=20 を保証済み。
	return fields[0], start, nil
}

// bootTime は /proc/stat の btime 行（システムのブート時刻、エポック秒）を読む。
func bootTime() (time.Time, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, fmt.Errorf("/proc/stat を読み取れません: %w", err)
	}
	for line := range strings.Lines(string(data)) {
		if rest, ok := strings.CutPrefix(line, "btime "); ok {
			secs, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
			if err != nil {
				return time.Time{}, fmt.Errorf("/proc/stat の btime %q を解析できません: %w", rest, err)
			}
			return time.Unix(secs, 0), nil
		}
	}
	return time.Time{}, errors.New("/proc/stat に btime が見つかりません")
}
