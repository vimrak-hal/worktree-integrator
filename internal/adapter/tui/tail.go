package tui

import (
	"bytes"
	"io"
	"os"
)

// initialWindow は初回読み込みで遡る最大バイト数。巨大なログでも TUI の起動・
// 切り替えが即座に完了するよう、末尾のこの範囲だけを読む（`tail` と同じ発想）。
const initialWindow = 64 * 1024

// tailer は 1 つのログファイルを増分読みする。tail -f の Go 実装であり、ポーリング
// ごとに前回オフセット以降の追記分だけを読む（`server logs` の全読みを毎ティック
// 繰り返さない）。ファイルの縮小（SpawnDetached のローテートや手動トランケート）を
// 検出すると先頭から読み直す。
type tailer struct {
	path string
	// offset は次回読み出しの開始位置。
	offset int64
	// partial は改行で終わっていない読みかけの行。次回の読み出しと連結される。
	partial []byte
	// primed は初回読み込み（末尾 initialWindow バイトへのシーク）が済んだかどうか。
	primed bool
}

func newTailer(path string) *tailer {
	return &tailer{path: path}
}

// poll は前回以降に追記された完全な行を返す。追記が無ければ nil を返す。ファイルが
// まだ存在しない場合もエラーにせず nil を返す（サーバー起動前のログは「まだ無い」が
// 正常な状態である）。
func (t *tailer) poll() ([]string, error) {
	f, err := os.Open(t.path)
	if err != nil {
		if os.IsNotExist(err) {
			// ファイルが（再）作成されたら先頭から読めるようにリセットしておく。
			t.offset = 0
			t.partial = nil
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()

	dropFirst := false
	if !t.primed {
		// 初回は末尾 initialWindow バイトだけを対象にする。途中から読み始めた場合、
		// 最初の行は途中からの断片なので捨てる。
		t.primed = true
		if size > initialWindow {
			t.offset = size - initialWindow
			dropFirst = true
		}
	}
	if size < t.offset {
		// ローテート／トランケート: 新しい内容を先頭から読み直す。
		t.offset = 0
		t.partial = nil
	}
	if size == t.offset {
		return nil, nil
	}

	if _, err := f.Seek(t.offset, io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, size-t.offset)
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, err
	}
	t.offset = size

	data := append(t.partial, buf...)
	t.partial = nil
	if dropFirst {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		} else {
			t.partial = data
			return nil, nil
		}
	}

	var lines []string
	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			break
		}
		lines = append(lines, string(bytes.TrimSuffix(data[:i], []byte("\r"))))
		data = data[i+1:]
	}
	if len(data) > 0 {
		t.partial = append([]byte(nil), data...)
	}
	return lines, nil
}

// ring は末尾 cap 行だけを保持する行バッファ。ログは無限に伸びるため、TUI が保持
// する範囲に上限を設ける。
type ring struct {
	cap   int
	lines []string
}

func newRing(cap int) *ring {
	return &ring{cap: cap}
}

// push は行を追記し、cap を超えた古い行を捨てる。
func (r *ring) push(ls ...string) {
	r.lines = append(r.lines, ls...)
	if len(r.lines) > r.cap {
		// 先頭を切り落とすだけだと下層配列が伸び続けるため、詰め直して解放する。
		trimmed := make([]string, r.cap)
		copy(trimmed, r.lines[len(r.lines)-r.cap:])
		r.lines = trimmed
	}
}

// slice は保持中の行を古い順に返す（呼び出し側は変更しないこと）。
func (r *ring) slice() []string {
	return r.lines
}
