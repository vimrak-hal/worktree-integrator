// Package app はワークフローの束ね役である。App 構造体が全ワークフロー共通の依存
// （設定・状態ルート・子プロセス IO・プロセス制御・対話セレクタ・進捗通知先）を
// 保持し、型付きメソッドが各ワークフロー（create / tree / server / alias / repos）を
// 駆動して型付きの Result を返す。CLI（main）と MCP（mcpserver）はどちらも同じ App
// メソッドを呼ぶ。整形（日本語のテキスト・JSON）は adapter/render が Result から
// 派生させ、app 層は io.Writer に一切書かない（旧 app.Run ルーター・app/output・
// app/alias は解体された）。
package app

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/vimrak-hal/worktree-integrator/internal/app/create"
	"github.com/vimrak-hal/worktree-integrator/internal/app/server"
	"github.com/vimrak-hal/worktree-integrator/internal/app/tree"
	"github.com/vimrak-hal/worktree-integrator/internal/core/action"
	corealias "github.com/vimrak-hal/worktree-integrator/internal/core/alias"
	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
	"github.com/vimrak-hal/worktree-integrator/internal/core/git/repo"
	"github.com/vimrak-hal/worktree-integrator/internal/core/git/worktree"
	coreserver "github.com/vimrak-hal/worktree-integrator/internal/core/server"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/childio"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/statedir"
)

// Progress はワークフローの途中経過の通知先である。汎用のイベントバスではなく、
// ワークフロー別の小さなコールバックの合成: create の並列進捗（worktree.Reporter と
// 構造的に一致する Update / Event）と、server switch / stop の逐次イベント通知
// （ServerEvent）のみを持つ。CLI は端末描画の実装（adapter/render）を、MCP は
// 取り込みバッファへ描画する同じ実装を注入する。
type Progress interface {
	// Update は 1 リポジトリの作成進捗の遷移（fetch 中 / 作成中）。
	Update(repo string, state worktree.Progress)
	// Event は 1 リポジトリの型付き途中経過イベント（コピーの失敗など）。
	Event(repo string, n worktree.Note)
	// ServerEvent は 1 サーバーのライフサイクルイベント（起動・停止など）。
	ServerEvent(repo, server string, ev coreserver.Event)
}

// App は全ワークフロー共通の依存の束である。main / mcpserver が一度だけ構築し、
// 型付きメソッドを通じてワークフローを駆動する。
type App struct {
	// Config は読み込み済みの設定ファイル。
	Config *config.File
	// Root は状態ルート（状態ストア・別名ストア・repo 操作ロックの導出元）。
	Root statedir.Root
	// ChildIO はフック・サーバーライフサイクルコマンドの子プロセスに与える標準
	// ストリーム（CLI: Inherit / MCP: Quiet）。
	ChildIO childio.Streams
	// Proc はプロセス制御のバックエンド（本番: coreserver.NewUnixProcess）。
	Proc coreserver.ProcessControl
	// Selector は create の対話的リポジトリ選択。nil = 非対話（MCP と、非 TTY の
	// CLI）。非対話の create は --repo / --all の明示が必須になる。
	Selector create.Selector
	// Progress は進捗通知先。nil = 無通知。
	Progress Progress
}

// reporter は Progress を worktree.Reporter として返す（nil なら nil のまま —
// create.Deps 側が無通知の実装に差し替える）。
func (a *App) reporter() worktree.Reporter {
	if a.Progress == nil {
		return nil
	}
	return a.Progress
}

// serverEvents は Progress のサーバーイベント通知をコールバックとして返す
// （nil なら nil — server.Deps 側が無視する）。
func (a *App) serverEvents() func(repo, server string, ev coreserver.Event) {
	if a.Progress == nil {
		return nil
	}
	return a.Progress.ServerEvent
}

// Create は worktree 作成ワークフローを実行する。エラー時も、そこまでの結果を
// 保持した Result を可能な限り返す。
func (a *App) Create(ctx context.Context, act action.Create) (*create.Result, error) {
	return create.Run(ctx, create.Deps{
		ChildIO:  a.ChildIO,
		Selector: a.Selector,
		Root:     a.Root,
		Reporter: a.reporter(),
	}, act)
}

// treeDeps は tree ワークフロー（list / enter / remove / doctor）共通の依存を構築
// する。これらは解決済みアクションを取らないため、ディレクトリの解決（設定と
// WT_* 環境変数）はここで行う。返される取得関数は serverDeps と同じ LegacyBackup の
// 通知である。
func (a *App) treeDeps() (tree.Deps, func() string, error) {
	reposDir, err := action.ReposDir("", a.Config, os.Getenv, os.UserHomeDir)
	if err != nil {
		return tree.Deps{}, nil, err
	}
	worktreesDir, err := action.WorktreesDir("", a.Config, os.Getenv, os.UserHomeDir)
	if err != nil {
		return tree.Deps{}, nil, err
	}
	store := coreserver.NewStateStore(a.Root)
	var legacyBak string
	store.OnLegacy = func(bak string) { legacyBak = bak }
	deps := tree.Deps{
		Proc:         a.Proc,
		Store:        store,
		Aliases:      corealias.NewStore(a.Root),
		Root:         a.Root,
		ChildIO:      a.ChildIO,
		Events:       a.serverEvents(),
		Config:       a.Config,
		ReposDir:     reposDir,
		WorktreesDir: worktreesDir,
	}
	return deps, func() string { return legacyBak }, nil
}

// List は worktree の一覧（inventory・別名・サーバー状態の統合ビュー）を返す。
func (a *App) List(ctx context.Context) (*tree.ListResult, error) {
	deps, legacy, err := a.treeDeps()
	if err != nil {
		return nil, err
	}
	res, err := tree.List(ctx, deps)
	if res != nil {
		res.LegacyBackup = legacy()
	}
	return res, err
}

// Enter は既存の worktree への遷移（after フックのみの実行）を行う。
func (a *App) Enter(ctx context.Context, name action.Name) (*tree.EnterResult, error) {
	deps, _, err := a.treeDeps()
	if err != nil {
		return nil, err
	}
	return tree.Enter(ctx, deps, name)
}

// Remove は worktree を完全な後始末付きで削除する。エラー時も、そこまでの結果を
// 保持した Result を可能な限り返す。
func (a *App) Remove(ctx context.Context, act action.Remove) (*tree.RemoveResult, error) {
	deps, legacy, err := a.treeDeps()
	if err != nil {
		return nil, err
	}
	res, err := tree.Remove(ctx, deps, act)
	if res != nil {
		res.LegacyBackup = legacy()
	}
	return res, err
}

// Doctor は自己診断を実行し、fix なら修復可能な発見をその場で修復する。
func (a *App) Doctor(ctx context.Context, fix bool) (*tree.DoctorResult, error) {
	deps, legacy, err := a.treeDeps()
	if err != nil {
		return nil, err
	}
	res, err := tree.Doctor(ctx, deps, fix)
	if res != nil {
		res.LegacyBackup = legacy()
	}
	return res, err
}

// serverDeps は server ワークフロー共通の依存を構築する。返される取得関数は、
// 旧形式の状態ファイルの退避（.bak）が起きた場合にその退避先を返す（Result の
// LegacyBackup フィールドへ写すため）。
func (a *App) serverDeps() (server.Deps, func() string) {
	store := coreserver.NewStateStore(a.Root)
	var legacyBak string
	store.OnLegacy = func(bak string) { legacyBak = bak }
	deps := server.Deps{
		Proc:    a.Proc,
		Store:   store,
		Aliases: corealias.NewStore(a.Root),
		Root:    a.Root,
		Events:  a.serverEvents(),
	}
	return deps, func() string { return legacyBak }
}

// ServerSwitch は対象リポジトリのサーバーを cmd/k の worktree へ切り替える。
func (a *App) ServerSwitch(ctx context.Context, cmd action.ServerCommand, k action.SwitchKind) (*server.SwitchResult, error) {
	deps, legacy := a.serverDeps()
	res, err := server.Switch(ctx, deps, cmd, k)
	if res != nil {
		res.LegacyBackup = legacy()
	}
	return res, err
}

// ServerStatus は（repo × server）ごとの状態を返す。
func (a *App) ServerStatus(ctx context.Context, cmd action.ServerCommand) (*server.StatusResult, error) {
	deps, legacy := a.serverDeps()
	res, err := server.Status(ctx, deps, cmd)
	if res != nil {
		res.LegacyBackup = legacy()
	}
	return res, err
}

// ServerStop は対象サーバーを停止する。
func (a *App) ServerStop(ctx context.Context, cmd action.ServerCommand, k action.StopKind) (*server.StopResult, error) {
	deps, legacy := a.serverDeps()
	res, err := server.Stop(ctx, deps, cmd, k)
	if res != nil {
		res.LegacyBackup = legacy()
	}
	return res, err
}

// ServerLogs は対象サーバーのログ末尾を読み取る。
func (a *App) ServerLogs(ctx context.Context, cmd action.ServerCommand, k action.LogsKind) (*server.LogsResult, error) {
	deps, legacy := a.serverDeps()
	res, err := server.Logs(ctx, deps, cmd, k)
	if res != nil {
		res.LegacyBackup = legacy()
	}
	return res, err
}

// AliasSet は worktree の表示用別名を設定し、正規化後に保存された値を返す
// （ラベルは最初の 1 行にトリムされるため、入力と異なりうる）。空のラベルは
// core/alias がエラーとして拒否する。
func (a *App) AliasSet(ctx context.Context, name action.Name, label string) (stored string, err error) {
	return corealias.NewStore(a.Root).Set(ctx, name.String(), label)
}

// AliasesResult は AliasList の結果。
type AliasesResult struct {
	// Aliases は worktree 名 → 表示ラベル。
	Aliases map[string]string `json:"aliases"`
}

// SortedNames は別名が付いた worktree 名をソート済みで返す（表示層の決定的な
// 描画順のため）。
func (r *AliasesResult) SortedNames() []string {
	names := make([]string, 0, len(r.Aliases))
	for name := range r.Aliases {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// AliasList はすべての表示用別名を返す。
func (a *App) AliasList(ctx context.Context) (*AliasesResult, error) {
	doc, err := corealias.NewStore(a.Root).Load(ctx)
	if err != nil {
		return nil, err
	}
	aliases := doc.Aliases
	if aliases == nil {
		aliases = map[string]string{}
	}
	return &AliasesResult{Aliases: aliases}, nil
}

// AliasRemove は worktree の表示用別名を削除し、存在していたかを返す。
func (a *App) AliasRemove(ctx context.Context, name action.Name) (existed bool, err error) {
	return corealias.NewStore(a.Root).Remove(ctx, name.String())
}

// ReposResult は ListRepos の結果。
type ReposResult struct {
	// ReposDir は探索したベースディレクトリ。
	ReposDir string `json:"repos_dir"`
	// Repos は探索された Git リポジトリ（名前順）。
	Repos []RepoInfo `json:"repos"`
}

// RepoInfo は探索された 1 つのリポジトリ。
type RepoInfo struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// ListRepos は repos_dir 直下の Git リポジトリの一覧を返す。repos_dir は設定と
// 環境変数（WT_REPOS_DIR）から解決する（このメソッドは解決済みアクションを取らない
// ため、環境変数の参照だけはここで行う）。
func (a *App) ListRepos(ctx context.Context) (*ReposResult, error) {
	dir, err := action.ReposDir("", a.Config, os.Getenv, os.UserHomeDir)
	if err != nil {
		return nil, err
	}
	repos, err := repo.Discover(ctx, dir)
	if err != nil {
		return nil, fmt.Errorf("リポジトリの探索に失敗しました（%s）: %w", dir, err)
	}
	res := &ReposResult{ReposDir: dir, Repos: []RepoInfo{}}
	for _, r := range repos {
		res.Repos = append(res.Repos, RepoInfo{Name: r.Name, Path: r.Path})
	}
	return res, nil
}
