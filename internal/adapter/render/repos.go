package render

import (
	"fmt"
	"io"

	"github.com/vimrak-hal/worktree-integrator/internal/app"
)

// Repos は探索されたリポジトリの一覧を、探索先ディレクトリのヘッダー付きで
// 書き出す。1 件も見つからない場合はその旨を書き出す。
func Repos(w io.Writer, res *app.ReposResult) {
	if len(res.Repos) == 0 {
		fmt.Fprintf(w, "リポジトリが見つかりません（%s）\n", res.ReposDir)
		return
	}
	fmt.Fprintf(w, "%s に %d 件のリポジトリ:\n", res.ReposDir, len(res.Repos))
	for _, r := range res.Repos {
		fmt.Fprintf(w, "- %s\t%s\n", r.Name, r.Path)
	}
}
