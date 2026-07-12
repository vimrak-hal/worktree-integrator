package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vimrak-hal/worktree-integrator/internal/adapter/cli"
	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
)

// このファイルは run() の薄い統合テスト（引数解析・実行モードの振り分け・終了コードの
// 写像・config check の配線）に絞る。各コマンドの dispatch と render の疎通確認は
// adapter/clirun のテスト（clirun_test.go）へ移した。

// isolate は HOME（$XDG_CONFIG_HOME 未設定時の設定ファイル探索先）・
// XDG_CONFIG_HOME（設定ファイルの探索先）・XDG_STATE_HOME（状態の保存先）を
// 一時ディレクトリへ向け、実環境（ユーザーの実際の ~/.config）を一切汚さずに run を
// 丸ごと実行できるようにする。
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("WT_REPOS_DIR", "")
	t.Setenv("WT_WORKTREES_DIR", "")
	t.Setenv("WT_REMOTE", "")
	t.Setenv("WT_CONCURRENCY", "")
}

// 正常に完了したコマンドは 0 を返し、ワークフローの出力は渡した stdout に書かれる。
func TestRunSucceedsWithZero(t *testing.T) {
	isolate(t)
	var stdout, stderr bytes.Buffer

	code := run(t.Context(), []string{"alias", "list"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "別名は登録されていません") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

// --help は HelpShown として run が受け取り、テキストは渡した stdout に書かれる
// （旧実装は cobra がプロセスの stdout へ直接書いていた）。
func TestRunHelpWritesToGivenStdout(t *testing.T) {
	isolate(t)
	var stdout, stderr bytes.Buffer

	code := run(t.Context(), []string{"--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Usage:") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

// 解析エラー（引数なしの素の実行）は 1 を返し、エラーは stderr に書かれる。
func TestRunParseErrorReturnsOne(t *testing.T) {
	isolate(t)
	var stdout, stderr bytes.Buffer

	code := run(t.Context(), nil, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "error:") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

// ワークフローのエラー（不正な worktree 名）は 1 を返す。
func TestRunWorkflowErrorReturnsOne(t *testing.T) {
	isolate(t)
	var stdout, stderr bytes.Buffer

	code := run(t.Context(), []string{"alias", "set", "bad name!", "label"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr: %q)", code, stderr.String())
	}
}

// キャンセル済みの ctx（Ctrl-C / SIGTERM 相当）でのワークフロー失敗は 130 を返す。
// server stop は状態ストアのロック取得で ctx を確認するため、キャンセルが
// context.Canceled のエラーとして表面化する。
func TestRunCanceledReturns130(t *testing.T) {
	isolate(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var stdout, stderr bytes.Buffer

	code := run(ctx, []string{"server", "stop"}, &stdout, &stderr)
	if code != 130 {
		t.Fatalf("exit code = %d, want 130 (stderr: %q)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "error:") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

// 対話プロンプトの中断（cli.ErrInterrupted）は、ctx がキャンセルされていなくても
// 130 に写像される（意図的な仕様変更: 旧実装は空選択と同一視して exit 0 だった）。
func TestExitCodeMapsPromptInterruptTo130(t *testing.T) {
	if got := exitCode(context.Background(), cli.ErrInterrupted); got != 130 {
		t.Fatalf("exitCode(ErrInterrupted) = %d, want 130", got)
	}
}

// (i) config check の 3 経路: ファイル不存在 / 正常 / 不正、を run() 経由（clirun.ConfigCheck
// の配線と終了コードの写像）で確認する。

// 設定ファイルが無ければ「既定値で動作します」で exit 0。
func TestRunConfigCheckMissingFile(t *testing.T) {
	isolate(t)
	var stdout, stderr bytes.Buffer

	code := run(t.Context(), []string{"config", "check"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "設定ファイルがありません") || !strings.Contains(stdout.String(), "既定値で動作します") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

// 存在し正常な設定ファイルは「設定は正常です」で exit 0。
func TestRunConfigCheckValidFile(t *testing.T) {
	isolate(t)
	writeConfigFile(t, "remote = \"origin\"\n")
	var stdout, stderr bytes.Buffer

	code := run(t.Context(), []string{"config", "check"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "設定は正常です") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

// 存在するが不正な設定ファイルはエラーを stderr に出して exit 1。
func TestRunConfigCheckInvalidFile(t *testing.T) {
	isolate(t)
	writeConfigFile(t, "[[hooks.before]]\ncommand = \"x\"\n")
	var stdout, stderr bytes.Buffer

	code := run(t.Context(), []string{"config", "check"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (stdout: %q)", code, stdout.String())
	}
	if !strings.Contains(stderr.String(), "`name` がありません") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

// writeConfigFile は isolate() が向けた XDG_CONFIG_HOME 配下の既定パスへ設定ファイルを
// 書き込む（実際の ~/.config には一切触れない）。
func writeConfigFile(t *testing.T, content string) string {
	t.Helper()
	path, ok := config.DefaultPath()
	if !ok {
		t.Fatal("could not resolve the default config path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
