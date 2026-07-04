// Package tree は worktree ライフサイクルの残り半分 — 一覧（list）・遷移（enter）・
// 削除（remove）・自己診断（doctor）— のワークフローを担う。作成（create）は
// app/create が担い、この 4 つが「作った worktree を一覧できない・消せない・壊れたら
// 直せない」を塞ぐ。
//
// 真実源は inventory（ファイルシステムと git の実体スキャン）であり、状態ファイル・
// 別名・ログはそれに突き合わせて掃除される（現実と照合する）。整形（日本語の
// テキスト・JSON）は adapter/render が担い、このパッケージは io.Writer に一切
// 書かない。
package tree

import (
	"os"

	"github.com/vimrak-hal/worktree-integrator/internal/app/server"
	corealias "github.com/vimrak-hal/worktree-integrator/internal/core/alias"
	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
	coreserver "github.com/vimrak-hal/worktree-integrator/internal/core/server"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/childio"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/statedir"
)

// Deps は tree ワークフロー群の依存の束。App が自身のフィールドと解決済みの
// ディレクトリから構築する。
type Deps struct {
	// Proc はプロセス制御のバックエンド（remove のサーバー停止・list / doctor の
	// 生存確認）。
	Proc coreserver.ProcessControl
	// Store は状態ストア。
	Store *coreserver.StateStore
	// Aliases は表示用別名のストア。
	Aliases *corealias.Store
	// Root は状態ルート（repo 操作ロック・ログディレクトリの導出元）。
	Root statedir.Root
	// ChildIO はフック（enter の after フック）の子プロセスに与える標準ストリーム。
	ChildIO childio.Streams
	// Events はサーバーイベント（remove の停止）の逐次通知先（nil 可）。
	Events func(repo, server string, ev coreserver.Event)
	// Config は読み込み済みの設定ファイル（サーバー定義・フック・[repos.<name>]）。
	Config *config.File
	// ReposDir / WorktreesDir は解決済みのベースディレクトリ。
	ReposDir     string
	WorktreesDir string
}

// serverDeps は remove のサーバー停止が再利用する app/server の依存束を構築する。
func (d Deps) serverDeps() server.Deps {
	return server.Deps{
		Proc:    d.Proc,
		Store:   d.Store,
		Aliases: d.Aliases,
		Root:    d.Root,
		Events:  d.Events,
	}
}

// isDir は path が既存のディレクトリかどうかを返す（シンボリックリンクをたどる）。
func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
