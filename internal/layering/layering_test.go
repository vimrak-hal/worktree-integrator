// Package layering は internal 配下のパッケージ依存が
// adapter → app → core → infra の一方向であることを検査する、テスト専用の
// パッケージである（プロダクションコードは持たない）。
//
// 不変条件: 低層のパッケージは高層のパッケージを import しない。層の順位は
//
//	infra < core < app < adapter
//
// で、上位の層は下位の任意の層（隣接に限らない）を import できるが、逆方向は
// 禁止する。同一層内の import は許可する。
//
// 層に属さない internal パッケージの扱い:
//   - buildinfo: バージョン文字列の単一情報源。どの層からも import 可（層の
//     判定から除外する）。自身は internal を一切 import しない不変条件も併せて
//     検査する。
//   - infra/testutil: テスト専用の Git リポジトリ生成ヘルパーだが infra 層に
//     属する。最下層のため、どのテストから import されても方向違反にはならず、
//     特別扱いは不要。
//   - このパッケージ（layering）自身も層に属さないが、internal を import しない
//     ため検査結果に影響しない。
package layering

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// layerRank は層の順位を表す。数値が小さいほど下位。
var layerRank = map[string]int{
	"infra":   1,
	"core":    2,
	"app":     3,
	"adapter": 4,
}

// layerOf は internal からの相対パス（例 "core/git/worktree"）が属する層の順位を
// 返す。第 2 戻り値は、そのパスが 4 層のいずれかに属するかどうか。属さない
// パッケージ（buildinfo・layering 自身など）では false を返す。
func layerOf(rel string) (rank int, layered bool) {
	head := rel
	if i := strings.IndexByte(rel, '/'); i >= 0 {
		head = rel[:i]
	}
	rank, layered = layerRank[head]
	return rank, layered
}

// TestLayerDependenciesAreOneDirectional は internal 配下の全 .go ファイル
// （_test.go を含む）の import を go/parser で走査し、低層のパッケージが高層の
// パッケージを import していないことを表明する。新しいパッケージを追加しても
// ディレクトリ走査により自動で検査対象になる。
func TestLayerDependenciesAreOneDirectional(t *testing.T) {
	root := repoRoot(t)
	// モジュールパスはハードコードせず go.mod から読む。
	internalPrefix := modulePath(t, root) + "/internal/"
	internalDir := filepath.Join(root, "internal")

	fset := token.NewFileSet()
	err := filepath.WalkDir(internalDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}

		// import 元パッケージの層は、internal からの相対ディレクトリで判定する。
		relDir, err := filepath.Rel(internalDir, filepath.Dir(path))
		if err != nil {
			return err
		}
		relDir = filepath.ToSlash(relDir)
		importerRank, importerLayered := layerOf(relDir)

		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range f.Imports {
			imp, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			if !strings.HasPrefix(imp, internalPrefix) {
				continue // 標準ライブラリ・サードパーティ・非 internal は対象外。
			}
			importedRel := strings.TrimPrefix(imp, internalPrefix)
			importedRank, importedLayered := layerOf(importedRel)

			// 層に属さない被 import パッケージ（buildinfo）は、どの層からでも
			// import してよい。
			if !importedLayered {
				continue
			}
			// 層に属さない import 元パッケージ（buildinfo 等）は、層付きの
			// internal パッケージを import してはならない。
			if !importerLayered {
				t.Errorf("レイヤ違反: 層に属さない %s が層付きの %s を import している", relDir, importedRel)
				continue
			}
			// 低層が高層を import していないこと。同一層は許可。
			if importerRank < importedRank {
				t.Errorf("レイヤ違反: %s が上位層の %s を import している", relDir, importedRel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("internal の走査に失敗: %v", err)
	}
}

// repoRoot は、この test ファイルの位置から上方向へ go.mod を探し、リポジトリ
// ルートの絶対パスを返す。作業ディレクトリに依存しない。
func repoRoot(t *testing.T) string {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("呼び出し元のファイルパスを取得できない")
	}
	dir := filepath.Dir(self)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod が見つからない")
		}
		dir = parent
	}
}

// modulePath は go.mod の module 行からモジュールパスを読み取る。テストがパスを
// ハードコードしないための単一情報源。
func modulePath(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(rest)
		}
	}
	t.Fatal("go.mod に module 行が無い")
	return ""
}
