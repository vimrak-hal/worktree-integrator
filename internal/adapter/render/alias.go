package render

import (
	"fmt"
	"io"

	"github.com/vimrak-hal/worktree-integrator/internal/app"
)

// Aliases はすべての表示用別名を、worktree 名で整列したテーブルとして書き出す。
// 1 件も無ければその旨を書き出す。
func Aliases(w io.Writer, res *app.AliasesResult) {
	if len(res.Aliases) == 0 {
		fmt.Fprintln(w, "別名は登録されていません")
		return
	}
	for _, name := range res.SortedNames() {
		fmt.Fprintf(w, "%s%s\n", PadRight(name, 24), res.Aliases[name])
	}
}

// AliasSet は別名の設定結果（正規化後に保存された値）を書き出す。
func AliasSet(w io.Writer, name, stored string) {
	fmt.Fprintf(w, "別名を設定しました: %s = %s\n", name, stored)
}

// AliasRemoved は別名の削除結果を書き出す。
func AliasRemoved(w io.Writer, name string, existed bool) {
	if existed {
		fmt.Fprintf(w, "別名を削除しました: %s\n", name)
	} else {
		fmt.Fprintf(w, "別名は登録されていません: %s\n", name)
	}
}
