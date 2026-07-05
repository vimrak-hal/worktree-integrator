// Package server はサーバーライフサイクルのワークフロー（switch / status / stop /
// logs）を担う。core/server のドメインロジック（プロセス制御・状態ストア）を駆動し、
// 型付きの Result を返す。整形（日本語のテキスト・JSON）は adapter/render が担い、
// このパッケージは io.Writer に一切書かない。途中経過（サーバーイベント）は
// Deps.Events コールバックで逐次通知される。core/server は同名衝突を避けるため
// coreserver として別名 import している。
//
// MCP 層と CLI は、いずれも App の型付きメソッド経由で同じワークフロー関数
// （Switch / Status / Stop / Logs）を駆動する。
//
// ロックは二段構え: リポジトリ操作ロック（statedir.Root.WithRepoLock）が switch /
// stop のワークフロー全体を同一リポジトリについて直列化し（プロセス跨ぎ）、状態
// ファイルロック（infra/store）は Load / Save の間だけの短命ロックとする。ロック
// 順序は常に「repo 操作ロック → 状態ファイルロック」で、status / logs は repo
// ロックを取らない（短命の状態ロックのみ）。
package server

import (
	"context"
	"fmt"
	"maps"
	"os"
	"slices"

	"github.com/vimrak-hal/worktree-integrator/internal/core/action"
	corealias "github.com/vimrak-hal/worktree-integrator/internal/core/alias"
	coreserver "github.com/vimrak-hal/worktree-integrator/internal/core/server"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/statedir"
)

// Deps は server ワークフロー群の依存の束。App が自身のフィールドから構築する。
type Deps struct {
	// Proc はプロセス制御のバックエンド（本番: UnixProcess、テスト: serverfake）。
	Proc coreserver.ProcessControl
	// Store は状態ストア。
	Store *coreserver.StateStore
	// Aliases は表示用別名のストア（status のみ使用）。
	Aliases *corealias.Store
	// Root は repo 操作ロックの取得に使う状態ルート。
	Root statedir.Root
	// Events はサーバーイベントの逐次通知先（nil 可）。CLI はライブ表示、MCP は
	// 取り込みバッファへの描画に使う。
	Events func(repo, server string, ev coreserver.Event)
}

// emit はイベントを通知する（Events が nil なら何もしない）。
func (d Deps) emit(repo, server string, ev coreserver.Event) {
	if d.Events != nil {
		d.Events(repo, server, ev)
	}
}

// repoFilter は --repo で選択されたリポジトリ名の集合。空のフィルタは「すべての
// リポジトリ」を意味する。
type repoFilter []string

// selected は name がこのフィルタで選択されているかどうかを返す。
func (f repoFilter) selected(name string) bool {
	return len(f) == 0 || slices.Contains(f, name)
}

// target は server コマンドの 1 対象リポジトリ。設定されたサーバー定義（Specs）と、
// 表示・停止の対象になるサーバー名の全集合（Names）を持つ。
type target struct {
	Repo string
	// Names は対象サーバー名: 設定にあるもの ∪ 状態に稼働記録があるもの（ソート済み）。
	// 設定から消えたが稼働記録が残るサーバーもここに現れ、status / stop から観測・
	// 操作できる。
	Names []string
	// Specs は設定にあるサーバー定義。設定の無い（状態にだけ現れる）リポジトリでは
	// nil。
	Specs coreserver.RepoServers
}

// resolveTargets は対象リポジトリの集合を「設定に servers があるもの ∪ 状態に稼働
// 記録があるもの」の和集合として解決し、--repo フィルタを適用する。設定にも状態にも
// 無い --repo 名は unknown として返す（対象が単に無いだけなのでエラーにはせず、
// 各 Result の UnknownRepos として表示層が警告する）。すべてのサブコマンドがこの
// 1 つの解決を通ることで、「設定を消したのに稼働し続けるサーバーが status / stop
// から見えない」という盲点を塞ぐ。
func resolveTargets(cmd action.ServerCommand, state *coreserver.State) (targets []target, unknown []string) {
	filter := repoFilter(cmd.Repos)

	// リポジトリ名の和集合。
	repoNames := map[string]bool{}
	for name, servers := range cmd.Servers {
		if len(servers) > 0 {
			repoNames[name] = true
		}
	}
	for name, rs := range state.Repos {
		for _, runtime := range rs.Servers {
			if runtime != nil && runtime.Running != nil {
				repoNames[name] = true
				break
			}
		}
	}

	for _, name := range slices.Sorted(maps.Keys(repoNames)) {
		if !filter.selected(name) {
			continue
		}
		specs, _ := cmd.Servers.GetRepo(name)
		serverNames := map[string]bool{}
		for s := range specs {
			serverNames[s] = true
		}
		if rs := state.Repos[name]; rs != nil {
			for s, runtime := range rs.Servers {
				if runtime != nil && runtime.Running != nil {
					serverNames[s] = true
				}
			}
		}
		targets = append(targets, target{
			Repo:  name,
			Names: slices.Sorted(maps.Keys(serverNames)),
			Specs: specs,
		})
	}

	for _, want := range cmd.Repos {
		if !repoNames[want] {
			unknown = append(unknown, want)
		}
	}
	return targets, unknown
}

// resolveUnderStateLock は短命の状態ロックの下で対象を解決する（switch / stop /
// logs が最初に呼ぶ共通ヘルパー）。
func resolveUnderStateLock(ctx context.Context, store *coreserver.StateStore, cmd action.ServerCommand) (targets []target, unknown []string, err error) {
	err = store.View(ctx, func(state *coreserver.State) error {
		targets, unknown = resolveTargets(cmd, state)
		return nil
	})
	return targets, unknown, err
}

// loadRuntimes は短命の状態ロックで repo のサーバー runtime 群を読み出す。View が
// 返すドキュメントは呼び出しごとの専有コピーであり、repo 操作ロックの下では他の
// 書き手がこの repo のエントリを変更しないため、ロック外へ持ち出して操作し、後で
// saveRuntime で書き戻して安全である。
func loadRuntimes(ctx context.Context, store *coreserver.StateStore, repo string) (map[string]*coreserver.Runtime, error) {
	runtimes := map[string]*coreserver.Runtime{}
	err := store.View(ctx, func(state *coreserver.State) error {
		if rs := state.Repos[repo]; rs != nil {
			for name, rt := range rs.Servers {
				if rt != nil {
					runtimes[name] = rt
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return runtimes, nil
}

// saveRuntime は短命の状態ロックで 1 つのサーバー runtime を書き戻す。他の
// リポジトリへの並行する書き込みとは、最新のファイルを読み直してから自分の
// エントリだけを差し替えることで共存する（自リポジトリの直列化は repo 操作ロックが
// 保証済み）。
func saveRuntime(ctx context.Context, store *coreserver.StateStore, repo, server string, runtime *coreserver.Runtime) error {
	return store.Update(ctx, func(state *coreserver.State) (bool, error) {
		state.Repo(repo).Servers[server] = runtime
		return true, nil
	})
}

// pathExists は path が存在するかどうかを返す（シンボリックリンクをたどる）。
func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// failedCountError は失敗件数を伝えるワークフローの最終エラーを構築する。
func failedCountError(failed int, what string) error {
	return fmt.Errorf("%d 件の%sに失敗しました", failed, what)
}
