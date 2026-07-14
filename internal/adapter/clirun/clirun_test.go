package clirun

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/vimrak-hal/worktree-integrator/internal/adapter/cli"
	"github.com/vimrak-hal/worktree-integrator/internal/adapter/render"
	"github.com/vimrak-hal/worktree-integrator/internal/app"
	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/childio"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/statedir"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/testutil"
)

// このファイルは、各コマンドの dispatch と render の疎通確認（旧 main_test.go の
// dispatch 相当のテスト）を、Run を直接駆動する形で持つ。終了コードの写像は main の
// 責務なので main_test.go 側で確認する。

// isolate は HOME / XDG_CONFIG_HOME / XDG_STATE_HOME を一時ディレクトリへ向け、実環境を
// 汚さずに Run を丸ごと実行できるようにする（main_test の同名ヘルパーと同じ役割）。
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

// newApp は隔離済み環境から CLI 用の App を構築する（main の run と同じ組み立て。
// テストの stdio は非 TTY のため Selector は nil のままで、対話選択を要する create は
// 「--repo か --all を指定してください」エラーになる）。progress は main と同じ流儀で、
// JSON 出力を要求しない起動のときだけ結線する（--json 経路は進捗テキストで stdout を
// 汚さない）。
func newApp(t *testing.T, stdout io.Writer, progress bool) *app.App {
	t.Helper()
	file, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	root, err := statedir.Default()
	if err != nil {
		t.Fatal(err)
	}
	var opts []app.Option
	if progress {
		opts = append(opts, app.WithProgress(render.NewProgress(stdout)))
	}
	return app.New(file, root, childio.Inherit(), opts...)
}

// runArgs は args を cli.Parse で Invocation へ解決し、隔離済み App で Run へ通す。
// 進捗描画の結線可否は main と同じく cli.JSONRequested で分岐する。
func runArgs(t *testing.T, ctx context.Context, args []string, stdout io.Writer) error {
	t.Helper()
	inv, err := cli.Parse(args)
	if err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return Run(ctx, inv, newApp(t, stdout, !cli.JSONRequested(inv)), stdout)
}

// tree 系コマンド（list / doctor / repos / enter / remove）の dispatch の疎通確認。
// 空の環境では list は空の一覧、doctor は発見なし、repos は「見つかりません」を出す。
// enter / remove は存在しない worktree に対してエラーになる。
func TestRunTreeCommands(t *testing.T) {
	isolate(t)
	t.Setenv("WT_REPOS_DIR", t.TempDir())
	t.Setenv("WT_WORKTREES_DIR", t.TempDir())

	t.Run("list --json", func(t *testing.T) {
		var stdout bytes.Buffer
		if err := runArgs(t, t.Context(), []string{"list", "--json"}, &stdout); err != nil {
			t.Fatalf("err = %v", err)
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
		var stdout bytes.Buffer
		if err := runArgs(t, t.Context(), []string{"list"}, &stdout); err != nil {
			t.Fatalf("err = %v", err)
		}
		if !strings.Contains(stdout.String(), "worktree はありません") {
			t.Fatalf("stdout = %q", stdout.String())
		}
	})
	t.Run("doctor", func(t *testing.T) {
		var stdout bytes.Buffer
		if err := runArgs(t, t.Context(), []string{"doctor"}, &stdout); err != nil {
			t.Fatalf("err = %v", err)
		}
		if !strings.Contains(stdout.String(), "問題は見つかりませんでした") {
			t.Fatalf("stdout = %q", stdout.String())
		}
	})
	t.Run("repos", func(t *testing.T) {
		var stdout bytes.Buffer
		if err := runArgs(t, t.Context(), []string{"repos"}, &stdout); err != nil {
			t.Fatalf("err = %v", err)
		}
		if !strings.Contains(stdout.String(), "リポジトリが見つかりません") {
			t.Fatalf("stdout = %q", stdout.String())
		}
	})
	t.Run("enter missing", func(t *testing.T) {
		var stdout bytes.Buffer
		err := runArgs(t, t.Context(), []string{"enter", "no-such"}, &stdout)
		if err == nil || !strings.Contains(err.Error(), "がありません") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("remove missing", func(t *testing.T) {
		var stdout bytes.Buffer
		if err := runArgs(t, t.Context(), []string{"remove", "no-such"}, &stdout); err == nil {
			t.Fatal("存在しない worktree の remove はエラーになるべき")
		}
	})
}

// server status --json は StatusResult をそのまま JSON で出力する（テーブル描画
// なし）。設定なしの初期状態では no_server_config が立つ。
func TestRunServerStatusJson(t *testing.T) {
	isolate(t)
	var stdout bytes.Buffer

	if err := runArgs(t, t.Context(), []string{"server", "status", "--json"}, &stdout); err != nil {
		t.Fatalf("err = %v", err)
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

// --json は型付き Result を返す全コマンドで機械可読出力を出す。ここでは代表として
// repos / server stop / create の JSON 経路を、JSON としてパースできて主要フィールドを
// 含むこと（かつテキストが混ざらないこと）で確認する（テキスト描画は他テストが担う）。
func TestRunJsonAcrossResultCommands(t *testing.T) {
	isolate(t)
	t.Setenv("WT_REPOS_DIR", t.TempDir())
	t.Setenv("WT_WORKTREES_DIR", t.TempDir())

	t.Run("repos --json", func(t *testing.T) {
		var stdout bytes.Buffer
		if err := runArgs(t, t.Context(), []string{"repos", "--json"}, &stdout); err != nil {
			t.Fatalf("err = %v", err)
		}
		var decoded struct {
			ReposDir string           `json:"repos_dir"`
			Repos    []map[string]any `json:"repos"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
			t.Fatalf("stdout is not JSON: %v\n%s", err, stdout.String())
		}
		if decoded.ReposDir == "" || len(decoded.Repos) != 0 {
			t.Fatalf("decoded = %+v", decoded)
		}
		if strings.Contains(stdout.String(), "リポジトリが見つかりません") {
			t.Fatalf("--json はテキストを描画しない: %q", stdout.String())
		}
	})

	t.Run("server stop --json", func(t *testing.T) {
		var stdout bytes.Buffer
		if err := runArgs(t, t.Context(), []string{"server", "stop", "--json"}, &stdout); err != nil {
			t.Fatalf("err = %v", err)
		}
		var decoded struct {
			Stopped int `json:"stopped"`
			Failed  int `json:"failed"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
			t.Fatalf("stdout is not JSON: %v\n%s", err, stdout.String())
		}
		if decoded.Stopped != 0 || decoded.Failed != 0 {
			t.Fatalf("decoded = %+v", decoded)
		}
	})

	t.Run("create --all --json", func(t *testing.T) {
		// リポジトリの無い repos_dir に対する --all は no_repositories で正常終了し
		// （エラー無し）、Result を返す — その JSON 経路を確認する。
		var stdout bytes.Buffer
		if err := runArgs(t, t.Context(), []string{"create", "feat-x", "--all", "--json"}, &stdout); err != nil {
			t.Fatalf("err = %v", err)
		}
		var decoded struct {
			Worktree    string `json:"worktree"`
			Disposition string `json:"disposition"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
			t.Fatalf("stdout is not JSON: %v\n%s", err, stdout.String())
		}
		if decoded.Worktree != "feat-x" || decoded.Disposition == "" {
			t.Fatalf("decoded = %+v", decoded)
		}
	})
}

// create --json は進捗描画を結線しない（runArgs が main と同じく JSONRequested で
// 分岐する）ため、実リポジトリを fetch しても stdout は進捗テキストで汚れず、最終 JSON
// のみになる。進捗を出す実体（repo-a）を用意し、--json の出力が JSON としてパースでき、
// かつ「fetch中 / 作成中」等の進捗行を一切含まないことを固定する（進捗が漏れると
// `| jq` が壊れる回帰の防止）。
func TestRunCreateJsonOmitsProgress(t *testing.T) {
	isolate(t)
	reposDir := t.TempDir()
	testutil.CloneWithBranchNamed(t, reposDir, "main", "repo-a")
	t.Setenv("WT_REPOS_DIR", reposDir)
	t.Setenv("WT_WORKTREES_DIR", t.TempDir())

	var stdout bytes.Buffer
	if err := runArgs(t, t.Context(), []string{"create", "feat-x", "--repo", "repo-a", "--json"}, &stdout); err != nil {
		t.Fatalf("err = %v", err)
	}
	var decoded struct {
		Worktree    string `json:"worktree"`
		Disposition string `json:"disposition"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout.String())
	}
	if decoded.Worktree != "feat-x" || decoded.Disposition == "" {
		t.Fatalf("decoded = %+v", decoded)
	}
	// 進捗テキスト（render.NewProgress の「  [repo] fetch中 / 作成中」）が混ざらない。
	for _, marker := range []string{"fetch中", "作成中", "  [repo-a]"} {
		if strings.Contains(stdout.String(), marker) {
			t.Fatalf("--json の stdout に進捗行 %q が混ざっている: %q", marker, stdout.String())
		}
	}
}

// 非 TTY（テストのバッファ stdio）での素の create は、--repo / --all の指定を促す
// エラーになる（対話プロンプトへは進まない）。
func TestRunCreateWithoutTTYRequiresExplicitRepos(t *testing.T) {
	isolate(t)
	t.Setenv("WT_REPOS_DIR", t.TempDir())
	t.Setenv("WT_WORKTREES_DIR", t.TempDir())
	var stdout bytes.Buffer

	err := runArgs(t, t.Context(), []string{"feat-x"}, &stdout)
	if err == nil || !strings.Contains(err.Error(), "--repo か --all を指定してください") {
		t.Fatalf("err = %v", err)
	}
}

// alias set → list の往復（Result 経由の描画）は stdout に日本語の案内を出す —
// render 経路の疎通確認。両呼び出しは同じ隔離環境（状態ルート）を共有する。
func TestRunAliasSetRoundTrip(t *testing.T) {
	isolate(t)
	var stdout bytes.Buffer

	if err := runArgs(t, t.Context(), []string{"alias", "set", "feat-x", "Login"}, &stdout); err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(stdout.String(), "別名を設定しました: feat-x = Login") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	stdout.Reset()
	if err := runArgs(t, t.Context(), []string{"alias", "list"}, &stdout); err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(stdout.String(), "feat-x") || !strings.Contains(stdout.String(), "Login") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}
