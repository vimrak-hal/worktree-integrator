package server_test

import (
	"reflect"
	"testing"

	"github.com/vimrak-hal/worktree-integrator/internal/core/cmdspec"
	"github.com/vimrak-hal/worktree-integrator/internal/core/server"
)

// TestConfigGetRepo は GetRepo が設定済みリポジトリを返し、未設定なら ok=false を返すことを確認する。
func TestConfigGetRepo(t *testing.T) {
	cfg := server.Config{
		"app": server.RepoServers{"backend": server.Spec{Start: cmdspec.FromString("x")}},
	}

	rs, ok := cfg.GetRepo("app")
	if !ok {
		t.Fatal("GetRepo(app) should be found")
	}
	if _, ok := rs["backend"]; !ok {
		t.Fatalf("returned RepoServers missing backend: %v", rs)
	}

	if _, ok := cfg.GetRepo("missing"); ok {
		t.Fatal("GetRepo(missing) should not be found")
	}
}

// TestConfigSortedRepoNames は、リポジトリ名がソート済みで返ることを確認する。
func TestConfigSortedRepoNames(t *testing.T) {
	spec := server.Spec{Start: cmdspec.FromString("x")}
	cfg := server.Config{
		"charlie": server.RepoServers{"s": spec},
		"alpha":   server.RepoServers{"s": spec},
		"bravo":   server.RepoServers{"s": spec},
	}

	got := cfg.SortedRepoNames()
	want := []string{"alpha", "bravo", "charlie"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SortedRepoNames() = %v, want %v", got, want)
	}

	if names := (server.Config{}).SortedRepoNames(); len(names) != 0 {
		t.Fatalf("empty config should yield no names, got %v", names)
	}
}

// TestRepoServersSortedServerNames は、サーバー名がソート済みで返ることを確認する。
func TestRepoServersSortedServerNames(t *testing.T) {
	spec := server.Spec{Start: cmdspec.FromString("x")}
	rs := server.RepoServers{
		"worker":   spec,
		"backend":  spec,
		"frontend": spec,
	}

	got := rs.SortedServerNames()
	want := []string{"backend", "frontend", "worker"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SortedServerNames() = %v, want %v", got, want)
	}

	if names := (server.RepoServers{}).SortedServerNames(); len(names) != 0 {
		t.Fatalf("empty RepoServers should yield no names, got %v", names)
	}
}

// TestSpecValidateRequiresStart は、start コマンドを欠くサーバー定義が拒否されることを
// 確認する。
func TestSpecValidateRequiresStart(t *testing.T) {
	if err := (server.Spec{}).Validate(); err == nil {
		t.Fatal("empty Spec should fail validation (missing start)")
	}
	if err := (server.Spec{Start: cmdspec.FromString("x")}).Validate(); err != nil {
		t.Fatalf("valid Spec should pass validation: %v", err)
	}
}

// TestConfigValidateReportsFirstMissingStart は、複数リポジトリ・複数サーバーに
// またがる start 欠落のうち、決定論的な順序（SortedRepoNames → SortedServerNames）で
// 最初に見つかったものを [repos.<repo>.servers.<server>] 付きで報告することを確認する。
func TestConfigValidateReportsFirstMissingStart(t *testing.T) {
	cfg := server.Config{
		"zebra": server.RepoServers{"web": server.Spec{}},
		"alpha": server.RepoServers{"api": server.Spec{}},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
	if got := err.Error(); got != "server [repos.alpha.servers.api] is missing its `start` command" {
		t.Fatalf("err = %q", got)
	}
}

// TestConfigValidateAcceptsWellFormedServers は、すべてのサーバーに start がある
// 設定を Validate が通過させることを確認する。
func TestConfigValidateAcceptsWellFormedServers(t *testing.T) {
	cfg := server.Config{
		"app": server.RepoServers{"backend": server.Spec{Start: cmdspec.FromString("x")}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
