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

// StartTime は pid の開始時刻を /proc/<pid>/stat の starttime（22 番目のフィールド、
// ブートからのクロックティック）と /proc/stat の btime（ブート時刻、エポック秒）から
// 合成する。プロセスが存在しない場合は ErrGone を返す。
func StartTime(pid int) (time.Time, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return time.Time{}, fmt.Errorf("pid %d: %w", pid, ErrGone)
		}
		return time.Time{}, fmt.Errorf("read /proc/%d/stat: %w", pid, err)
	}
	// 2 番目のフィールド comm は空白や括弧を含み得るため、位置での分割は最後の ')'
	// より後ろに対して行う。starttime はマニュアル上 22 番目 = ')' 以降の 20 番目。
	rest := string(data)
	if i := strings.LastIndexByte(rest, ')'); i >= 0 {
		rest = rest[i+1:]
	}
	fields := strings.Fields(rest)
	if len(fields) < 20 {
		return time.Time{}, fmt.Errorf("parse /proc/%d/stat: unexpected format", pid)
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse /proc/%d/stat starttime %q: %w", pid, fields[19], err)
	}
	boot, err := bootTime()
	if err != nil {
		return time.Time{}, err
	}
	return boot.Add(time.Duration(ticks) * (time.Second / userHZ)), nil
}

// bootTime は /proc/stat の btime 行（システムのブート時刻、エポック秒）を読む。
func bootTime() (time.Time, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, fmt.Errorf("read /proc/stat: %w", err)
	}
	for line := range strings.Lines(string(data)) {
		if rest, ok := strings.CutPrefix(line, "btime "); ok {
			secs, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
			if err != nil {
				return time.Time{}, fmt.Errorf("parse /proc/stat btime %q: %w", rest, err)
			}
			return time.Unix(secs, 0), nil
		}
	}
	return time.Time{}, errors.New("btime not found in /proc/stat")
}
