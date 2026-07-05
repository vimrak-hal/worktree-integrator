// Package buildinfo は、バイナリが名乗るバージョン文字列の単一情報源である。
//
// 解決順序は次のとおり:
//
//  1. ビルド時の -ldflags "-X .../internal/buildinfo.version=v1.2.3" による注入
//  2. runtime/debug.ReadBuildInfo が報告するメインモジュールのバージョン
//     （`go install module@version` でビルドされた場合に埋まる）
//  3. 上記いずれも得られない場合のフォールバック定数
package buildinfo

import "runtime/debug"

// version はリンカで注入されるバージョン文字列。ビルドスクリプトが
//
//	go build -ldflags "-X github.com/vimrak-hal/worktree-integrator/internal/buildinfo.version=v1.2.3"
//
// のように設定する。未注入の場合は空のまま残り、ReadBuildInfo へフォールバックする。
var version string

// fallbackVersion は、注入もモジュール情報も無い（例: リポジトリ内での素の
// `go build` / `go run`）場合に報告するバージョン。旧実装のハードコード値を
// 引き継いでいる。
const fallbackVersion = "0.1.0"

// Version はこのビルドのバージョン文字列を返す。常に非空である。
func Version() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		// ソースツリーから直接ビルドした場合、モジュールバージョンは "(devel)" と
		// なり、ユーザー向けのバージョンとしては役に立たないため採用しない。
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return fallbackVersion
}
