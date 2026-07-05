package server

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/vimrak-hal/worktree-integrator/internal/core/action"
	coreserver "github.com/vimrak-hal/worktree-integrator/internal/core/server"
)

// Logs は対象サーバーのログ末尾を読み取り、型付きの結果として返す。フォロー
// （tail -f）はこのワークフローには存在しない: CLI が LogEntry.Path を受けて自前で
// tail -f を実行する（MCP からは型レベルで到達不能）。
func Logs(ctx context.Context, d Deps, cmd action.ServerCommand, k action.LogsKind) (*LogsResult, error) {
	// 対象と状態は短命ロックの下で一度に読む。ログパスの解決は Instance.Log
	//（state の記録値）を正とし、決定的パスの再計算はフォールバックである。
	resolved, unknown, err := resolveUnderStateLock(ctx, d.Store, cmd)
	if err != nil {
		return nil, err
	}
	res := &LogsResult{UnknownRepos: unknown}

	// runtime のスナップショット（repo → server → *Runtime）。読み取り専用。
	runtimes := map[string]map[string]*coreserver.Runtime{}
	if err := d.Store.View(ctx, func(state *coreserver.State) error {
		for repoName, rs := range state.Repos {
			m := map[string]*coreserver.Runtime{}
			for name, rt := range rs.Servers {
				if rt != nil {
					m[name] = rt
				}
			}
			runtimes[repoName] = m
		}
		return nil
	}); err != nil {
		return nil, err
	}

	// generation は --prev のとき 1 世代前のログ（SpawnDetached がローテートした
	// .prev）へ写す。
	generation := func(path string) string {
		if k.Prev {
			return coreserver.PrevLogPath(path)
		}
		return path
	}

	switch scope := k.Scope.(type) {
	case action.OneWorktree:
		// 名前付き worktree。サーバーが現在稼働中かどうかに関わらず解決する。その
		// worktree で稼働中のインスタンスがあれば記録された Log を正とし、無ければ
		// 決定的なログパスへフォールバックする。存在しないログも Missing エントリと
		// して結果に現れる（表示層が案内する）。
		name := scope.Name.String()
		for _, tg := range resolved {
			for _, serverName := range tg.Names {
				path := d.Store.LogPath(tg.Repo, serverName, name)
				if rt := runtimes[tg.Repo][serverName]; rt != nil && rt.Running != nil && rt.Running.Worktree == name {
					path = rt.Running.Log
				}
				path = generation(path)
				res.Logs = append(res.Logs, readEntry(tg.Repo, serverName, path, k.Lines))
			}
		}
	case action.AllWorktrees:
		// 名前なし。各サーバーで現在稼働中のインスタンスのログ。稼働していなければ
		// 最後に起動したログ（Runtime.LastLog）へフォールバックし、クラッシュ直後
		// でも原因ログへ辿り着けるようにする。存在しないログは黙ってスキップする。
		for _, tg := range resolved {
			for _, serverName := range tg.Names {
				rt := runtimes[tg.Repo][serverName]
				if rt == nil {
					continue
				}
				var path string
				switch {
				case rt.Running != nil:
					path = rt.Running.Log
				case rt.LastLog != "":
					path = rt.LastLog
				default:
					continue
				}
				path = generation(path)
				if !pathExists(path) {
					continue
				}
				res.Logs = append(res.Logs, readEntry(tg.Repo, serverName, path, k.Lines))
			}
		}
	default:
		panic(fmt.Sprintf("unknown action.WorktreeScope %T", k.Scope))
	}

	return res, nil
}

// readEntry は 1 つのログファイルの末尾 n 行を読み取って LogEntry に写す。
// 存在しなければ Missing、読めなければ Error を立てる。
func readEntry(repo, server, path string, lines int) LogEntry {
	entry := LogEntry{Repo: repo, Server: server, Path: path}
	if !pathExists(path) {
		entry.Missing = true
		return entry
	}
	data, err := os.ReadFile(path)
	if err != nil {
		entry.Error = err.Error()
		return entry
	}
	entry.Lines = tailLines(data, lines)
	return entry
}

// tailLines は data を "\n" で分割した末尾 n 行を返す。末尾の改行は無視され、
// 余計な空の最終行を生じさせない。n が 0 以下なら何も返さない。
func tailLines(data []byte, n int) []string {
	if n <= 0 {
		return nil
	}
	text := strings.TrimRight(string(data), "\n")
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	if start := len(lines) - n; start > 0 {
		lines = lines[start:]
	}
	return lines
}
