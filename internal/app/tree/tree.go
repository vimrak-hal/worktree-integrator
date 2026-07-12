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
	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/childio"
)

// Deps は tree ワークフロー群の依存の束。App が自身のフィールドと解決済みの
// ディレクトリから構築する。server ワークフローと共通の依存（Proc / Store / Aliases /
// Root / Events）は server.Deps を埋め込んで共有し、tree 固有の依存だけを重ねる。
// フィールド追加時の同期箇所を 1 つに保つための埋め込みである。
type Deps struct {
	// Deps は server ワークフロー共通の依存（Proc / Store / Aliases / Root / Events）。
	// remove のサーバー停止がこの束をそのまま再利用する。
	server.Deps
	// ChildIO はフック（enter の after フック）の子プロセスに与える標準ストリーム。
	ChildIO childio.Streams
	// Config は読み込み済みの設定ファイル（サーバー定義・フック・[repos.<name>]）。
	Config *config.File
	// ReposDir / WorktreesDir は解決済みのベースディレクトリ。
	ReposDir     string
	WorktreesDir string
}

// serverDeps は remove のサーバー停止が再利用する app/server の依存束を返す（埋め込んだ
// server.Deps をそのまま返すだけ）。
func (d Deps) serverDeps() server.Deps {
	return d.Deps
}

// isDir は path が既存のディレクトリかどうかを返す（シンボリックリンクをたどる）。
func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
