package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vimrak-hal/worktree-integrator/internal/adapter/cli"
	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
)

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

// server status --json は StatusResult をそのまま JSON で出力する（テーブル描画
// なし）。設定なしの初期状態では no_server_config が立つ。
func TestRunServerStatusJson(t *testing.T) {
	isolate(t)
	var stdout, stderr bytes.Buffer

	code := run(t.Context(), []string{"server", "status", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr.String())
	}
	var decoded struct {
		Rows           []map[string]any `json:"rows"`
		NoServerConfig bool             `json:"no_server_config"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout.String())
	}
	if !decoded.NoServerConfig || len(decoded.Rows) != 0 {
		t.Fatalf("decoded = %+v", decoded)
	}
	if strings.Contains(stdout.String(), "REPO") {
		t.Fatalf("--json はテーブルを描画しない: %q", stdout.String())
	}
}

// (i) config check の 3 経路: ファイル不存在 / 正常 / 不正、をそれぞれ確認する。

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
	if !strings.Contains(stderr.String(), "is missing its `name`") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

// tree 系コマンド（list / doctor / repos / enter / remove）の main ディスパッチの
// 疎通確認。空の環境では list は空の一覧、doctor は発見なしで exit 0、repos は
// 「見つかりません」を出す。enter / remove は存在しない worktree に対して exit 1。
func TestRunTreeCommands(t *testing.T) {
	isolate(t)
	t.Setenv("WT_REPOS_DIR", t.TempDir())
	t.Setenv("WT_WORKTREES_DIR", t.TempDir())

	t.Run("list --json", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := run(t.Context(), []string{"list", "--json"}, &stdout, &stderr); code != 0 {
			t.Fatalf("exit code = %d (stderr: %q)", code, stderr.String())
		}
		var decoded struct {
			Worktrees []map[string]any `json:"worktrees"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
			t.Fatalf("stdout is not JSON: %v\n%s", err, stdout.String())
		}
		if decoded.Worktrees == nil || len(decoded.Worktrees) != 0 {
			t.Fatalf("decoded = %+v", decoded)
		}
	})
	t.Run("list", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := run(t.Context(), []string{"list"}, &stdout, &stderr); code != 0 {
			t.Fatalf("exit code = %d (stderr: %q)", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "worktree はありません") {
			t.Fatalf("stdout = %q", stdout.String())
		}
	})
	t.Run("doctor", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := run(t.Context(), []string{"doctor"}, &stdout, &stderr); code != 0 {
			t.Fatalf("exit code = %d (stderr: %q)", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "問題は見つかりませんでした") {
			t.Fatalf("stdout = %q", stdout.String())
		}
	})
	t.Run("repos", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := run(t.Context(), []string{"repos"}, &stdout, &stderr); code != 0 {
			t.Fatalf("exit code = %d (stderr: %q)", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "リポジトリが見つかりません") {
			t.Fatalf("stdout = %q", stdout.String())
		}
	})
	t.Run("enter missing", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := run(t.Context(), []string{"enter", "no-such"}, &stdout, &stderr); code != 1 {
			t.Fatalf("exit code = %d", code)
		}
		if !strings.Contains(stderr.String(), "がありません") {
			t.Fatalf("stderr = %q", stderr.String())
		}
	})
	t.Run("remove missing", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := run(t.Context(), []string{"remove", "no-such"}, &stdout, &stderr); code != 1 {
			t.Fatalf("exit code = %d", code)
		}
	})
}

// 非 TTY（テストのバッファ stdio）での素の create は、--repo / --all の指定を促す
// エラーで exit 1 になる（対話プロンプトへは進まない）。
func TestRunCreateWithoutTTYRequiresExplicitRepos(t *testing.T) {
	isolate(t)
	t.Setenv("WT_REPOS_DIR", t.TempDir())
	t.Setenv("WT_WORKTREES_DIR", t.TempDir())
	var stdout, stderr bytes.Buffer

	code := run(t.Context(), []string{"feat-x"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d (stderr: %q)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--repo か --all を指定してください") {
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

// alias set → list の往復（Result 経由の描画）は stdout に日本語の案内を出す —
// render 経路の疎通確認。
func TestRunAliasSetRoundTrip(t *testing.T) {
	isolate(t)
	var stdout, stderr bytes.Buffer

	if code := run(t.Context(), []string{"alias", "set", "feat-x", "Login"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d (stderr: %q)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "別名を設定しました: feat-x = Login") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	stdout.Reset()
	if code := run(t.Context(), []string{"alias", "list"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d (stderr: %q)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "feat-x") || !strings.Contains(stdout.String(), "Login") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}
