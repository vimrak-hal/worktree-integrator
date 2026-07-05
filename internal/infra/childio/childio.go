// Package childio は、このツールが起動するプロセス（フック、サーバーの
// ライフサイクルコマンド）の標準ストリームの接続先を記述する。
//
// CLI では、子プロセスはターミナルを継承するため、ユーザーはその出力を見ることが
// できる。MCP サーバーのもとでは、stdin/stdout が JSON-RPC プロトコルを運ぶため、
// 子プロセスは決してそれらに触れてはならない。stdin は /dev/null となり、出力は
// stderr に送られる（クライアントのログで見える）。Go の os/exec は nil の
// ストリームを /dev/null に接続するが、継承に頼らず明示的に設定する。
package childio

import (
	"io"
	"os"
)

// Streams は、起動されるフォアグラウンド／バックグラウンドの子プロセスに与える標準
// ストリームの集合を表す。Stdin が nil の場合、子プロセスは /dev/null から読み込む。
type Streams struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Inherit は、子プロセスをツール自身のターミナルに接続する（CLI のデフォルト）。
func Inherit() Streams {
	return Streams{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}
}

// Quiet は、子プロセスを stdin/stdout から切り離す。stdin/stdout が MCP プロトコルを
// 運ぶ場合に使われる。子プロセスは何も読み込まず（nil → /dev/null）、stderr に書き込む。
func Quiet() Streams {
	return Streams{Stdin: nil, Stdout: os.Stderr, Stderr: os.Stderr}
}
