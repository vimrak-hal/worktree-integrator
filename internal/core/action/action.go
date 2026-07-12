// Package action は解決済みコマンドの語彙と、その解決処理を保持する。すなわち、
// ユーザーが要求した内容を検証し完全に解決した表現（この語彙）と、フロントエンドの
// オーバーライドおよび設定ファイルからそれへの解決処理である（resolve.go を参照）。
//
// 各フロントエンドは生の入力を Overrides に変換し、ここのコンストラクタを呼び出す。
// パッケージ cli はコマンドラインフラグから、パッケージ mcpserver はツール呼び出しの
// パラメータから行う。続いてパッケージ app が結果の型を消費する。語彙とその解決処理の
// 両方を1つの中立的なパッケージにまとめることで、どちらのフロントエンドももう一方に
// 依存する必要がなくなる。
//
// ディスパッチはこの語彙を横断する単一のインターフェースではなく、cli.Invocation の
// 型スイッチ（main）と、それが振り分ける App の型付きメソッド（Create / Remove /
// ServerSwitch / AliasSet など）が行う。server / alias のように 1 つの起動要求が複数の
// 操作を束ねるものだけは、ServerKind / AliasKind の封印された sum-type で網羅性を型に
// 守らせる。
//
// worktree 名はすべて Name 型（name.go）で表現される。Name は ParseName でのみ
// 構築できるため、この語彙に「未検証の名前」が入り込む余地はない。
package action

import (
	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
	"github.com/vimrak-hal/worktree-integrator/internal/core/hooks"
	"github.com/vimrak-hal/worktree-integrator/internal/core/server"
)

// Remove は worktree 削除の解決済みコマンドである（`remove <name>`）。ディレクトリの
// 解決は App が設定・環境変数から行うため、ここには削除の意図だけを持つ。
type Remove struct {
	// Name は削除する worktree（およびブランチ）の検証済みの名前。
	Name Name
	// Force は dirty なチェックアウトの削除拒否（git worktree remove の安全弁）を
	// 上書きする。CLI 専用であり、MCP の worktree_remove には対応するパラメータが
	// 存在しない（LLM に dirty の強制削除を許さない — 意図的な非公開）。
	Force bool
	// KeepBranch は worktree 削除後のブランチ削除（git branch -D <name>）を
	// スキップする。
	KeepBranch bool
}

// Create は create ワークフローの解決済み設定である（旧名 Config。config.File との
// 紛らわしさを避けるため、ワークフロー名に合わせて改名した）。
type Create struct {
	// WorktreeName は作成する worktree（およびブランチ）の検証済みの名前。
	WorktreeName Name
	// Repos は非対話で明示されたリポジトリ名（CLI の --repo / MCP の repos）。
	// 空の場合、All が false であれば対話的に選択する。存在しない名前の検出は
	// ワークフロー（app/create）が探索結果と照合して行う。
	Repos []string
	// All はすべての探索済みリポジトリを対象とする（CLI の --all）。
	All          bool
	ReposDir     string
	WorktreesDir string
	Remote       string
	// Concurrency は並列度の上限。0 は「自動決定」を意味する（選択されたリポジトリの
	// 数に基づいて worktree.Concurrency が決める）。
	Concurrency int
	// Hooks は設定ファイルから読み込んだライフサイクルフックである。
	Hooks hooks.Config
	// Base は CLI の --base フラグ / MCP worktree_create の base パラメータで
	// 明示されたベースブランチのオーバーライド。空は「未指定」を意味し、解決は
	// BaseFor がリポジトリごとに行う（[repos.<name>].base → [defaults].base →
	// "auto" の順にフォールスルーする）。
	Base string
	// Defaults / RepoConfigs は設定ファイルの [defaults] / [repos.<name>] を
	// そのまま保持する。base・copy の解決はどちらもリポジトリが決まって初めて
	// 行えるため（対話選択モードでは Create 構築時点でまだ分からない）、
	// BaseFor / CopyPlanFor がワークフロー側（app/create）から呼び出される。
	Defaults    config.Defaults
	RepoConfigs map[string]config.RepoConfig
}

// BaseFor は repo のベースブランチ解決を行う: --base フラグ / MCP の base パラメータ
// （Create.Base）> [repos.<repo>].base > [defaults].base > "auto"。"auto" は
// internal/core/git.DefaultBranch による自動解決（symbolic-ref → main → master）を
// 意味する。
func (c Create) BaseFor(repo string) string {
	if c.Base != "" {
		return c.Base
	}
	if rc, ok := c.RepoConfigs[repo]; ok && rc.Base != "" {
		return rc.Base
	}
	if c.Defaults.Base != "" {
		return c.Defaults.Base
	}
	return config.DefaultBase
}

// CopyPlanFor は repo の copy 計画を、[defaults.copy] と [repos.<repo>.copy] を
// マージして解決する。
func (c Create) CopyPlanFor(repo string) config.CopyPlan {
	return config.MergeCopy(c.Defaults.Copy, c.RepoConfigs[repo].Copy)
}

// ServerCommand は server コマンドに共通する解決済みの実行コンテキスト
// （ディレクトリ・サーバー設定・リポジトリフィルタ）である。どの操作かは
// ServerKind が表し、App の型付きメソッド（ServerSwitch / ServerStatus /
// ServerStop / ServerLogs）へ操作固有の引数として別途渡される（旧実装は Kind を
// このコマンドに埋め込み、単一の Run ルーターが型スイッチしていた）。
type ServerCommand struct {
	ReposDir     string
	WorktreesDir string
	Servers      server.Config
	// Repos は --repo / MCP の repos による対象リポジトリの絞り込み。空はすべての
	// 設定済みリポジトリを意味する。各要素はパスコンポーネントとして検証済み。
	Repos []string
}

// ServerKind は server コマンドの操作を表す封印された sum-type である。実装は以下の
// 4 つの操作型のみで、非公開のマーカーメソッドによって封印されている。これにより
// 操作ごとに有効な引数だけを型で表現でき（例えば status に行数を、stop に restart を
// 渡すような不正な状態は構築できない）、CLI の Invocation に載って main が対応する
// App メソッドへディスパッチする際、型スイッチはすべてのケースを網羅する。
// 埋め込まれる worktree 名は Name 型のため、構築時点で検証済みである。
type ServerKind interface {
	isServerKind()
}

// SwitchKind は `server switch`。Name は切り替え先の worktree 名で必須である。
type SwitchKind struct {
	Name            Name
	RequireWorktree bool
	Restart         bool
}

// StatusKind は `server status`。worktree 名を持たない。
type StatusKind struct{}

// StopKind は `server stop`。Scope が停止対象の worktree を絞り込む。
type StopKind struct {
	Scope WorktreeScope
}

// LogsKind は `server logs`。Scope が対象の worktree を絞り込む。フォロー
// （tail -f）は意図的にこの語彙に存在しない: それは CLI 専用の表示手段であり、
// CLI が LogsResult のパス情報を受けて自前で tail -f を実行する（MCP からは
// 型レベルで到達不能）。
type LogsKind struct {
	Scope WorktreeScope
	// Lines は表示する末尾の行数。0 以下は「何も表示しない」として扱われる
	// （フロントエンドが既定 50 を与える）。
	Lines int
	// Prev は現行ログの代わりに 1 世代前のログ（起動時にローテートされた .prev）を
	// 参照する（CLI の --prev / MCP の prev）。
	Prev bool
}

func (SwitchKind) isServerKind() {}
func (StatusKind) isServerKind() {}
func (StopKind) isServerKind()   {}
func (LogsKind) isServerKind()   {}

// WorktreeScope は stop / logs の対象範囲を表す封印された sum-type である。
// 全 worktree を対象とする AllWorktrees と、単一の worktree に限定する OneWorktree を
// 型で区別する。空文字列のセンチネルは存在しない。全件対象は「引数の省略」のみで
// 表現され、空文字列はフロントエンドの ParseName が不正名として拒否する。
type WorktreeScope interface {
	isScope()
}

// AllWorktrees はすべての worktree を対象とする（worktree フィルタなし）。
type AllWorktrees struct{}

// OneWorktree は単一の worktree 名に限定する。
type OneWorktree struct{ Name Name }

func (AllWorktrees) isScope() {}
func (OneWorktree) isScope()  {}

// AliasKind は alias 操作を表す封印された sum-type である。実装は以下の 3 つの操作型
// のみで、非公開のマーカーメソッドによって封印されている。各操作は cli.Alias.Kind に
// 載って main の型スイッチに渡り、対応する App メソッド（AliasSet / AliasList /
// AliasRemove）へ振り分けられる（旧 AliasCommand ラッパは廃止）。
type AliasKind interface {
	isAliasKind()
}

// AliasSet は別名の設定。空のラベルはワークフロー（core/alias の Store.Set）が
// エラーとして拒否する。削除は AliasRemove の 1 経路のみである。
type AliasSet struct {
	Name  Name
	Value string
}

// AliasList はすべての別名の一覧（旧 alias get は list に統合された）。
type AliasList struct{}

// AliasRemove は別名の削除。
type AliasRemove struct{ Name Name }

func (AliasSet) isAliasKind()    {}
func (AliasList) isAliasKind()   {}
func (AliasRemove) isAliasKind() {}
