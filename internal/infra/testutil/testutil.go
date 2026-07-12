// Package testutil は、ユニットテスト向けの共通ヘルパーを提供する。`git` コマンドを
// 介してローカルの Git リポジトリを構築する（そのためテストがネットワークや SSH に
// 触れることはない）。
package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
)

var counter atomic.Int64

func nextID() int64 { return counter.Add(1) }

// git は dir（dir が空の場合はカレントディレクトリ）で `git <args...>` を実行する。
// 開発者のグローバル／システムの git 設定から隔離され、エラー時にはテストを失敗させる。
func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

// CloneWithBranchNamed は、branch 上に単一のコミットを持つ（HEAD をそれに設定した）
// ベアリモートを作成し、それを parent/<name> にクローンして、クローンのパスを返す。
// ベアリモートは、リポジトリ探索が無視する隠しの兄弟ディレクトリに置かれる（.git
// エントリを持たない）。
func CloneWithBranchNamed(t *testing.T, parent, branch, name string) string {
	t.Helper()
	id := nextID()
	// ベアリモートは、リポジトリ探索が無視する隠しの兄弟ディレクトリに置かれる
	// （ベアリポジトリは .git エントリを持たない）。
	remoteDir := filepath.Join(parent, ".remote-"+name+"-"+strconv.FormatInt(id, 10))
	// コミットを構築するための使い捨てのワーキングツリーは parent の下に置いては
	// ならない。リポジトリとして探索されてしまうためである（.git を持つ）。parent の
	// 外に構築し、コミットをプッシュしたら削除する。
	work, err := os.MkdirTemp("", "wti-work-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(work) }()

	git(t, "", "init", "-b", branch, work)
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, work, "add", "-A")
	git(t, work, "commit", "-m", "init")

	git(t, "", "init", "--bare", "-b", branch, remoteDir)
	git(t, work, "remote", "add", "origin", remoteDir)
	git(t, work, "push", "origin", branch)
	git(t, remoteDir, "symbolic-ref", "HEAD", "refs/heads/"+branch)

	repoPath := filepath.Join(parent, name)
	git(t, "", "clone", remoteDir, repoPath)
	return repoPath
}

// CloneWithBranch は、名前を自動生成する CloneWithBranchNamed と同様の関数である。
func CloneWithBranch(t *testing.T, parent, branch string) string {
	t.Helper()
	return CloneWithBranchNamed(t, parent, branch, "repo-"+strconv.FormatInt(nextID(), 10))
}

// Git は、dir で任意の git コマンドを実行し、エラー時にはテストを失敗させる。
// テストが追加のリポジトリ状態（例: ブランチの事前作成）をセットアップできるよう
// 公開されている。
func Git(t *testing.T, dir string, args ...string) {
	t.Helper()
	git(t, dir, args...)
}
