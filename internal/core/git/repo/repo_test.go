package repo

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDiscoverFindsOnlyGitDirsSorted(t *testing.T) {
	base := t.TempDir()
	// 2 つのリポジトリ: 1 つは .git ディレクトリを持ち、もう 1 つは .git ファイルを持つ。
	for _, name := range []string{"zebra", "alpha"} {
		if err := os.MkdirAll(filepath.Join(base, name, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(base, "linked"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "linked", ".git"), []byte("gitdir: /elsewhere"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 通常のディレクトリと通常のファイル: どちらもリポジトリではない。
	if err := os.MkdirAll(filepath.Join(base, "not-a-repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "loose-file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	found, err := Discover(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(found))
	for i, r := range found {
		names[i] = r.Name
	}
	if !reflect.DeepEqual(names, []string{"alpha", "linked", "zebra"}) {
		t.Fatalf("names = %v", names)
	}
}

func TestDiscoverMissingBaseDirIsError(t *testing.T) {
	if _, err := Discover(t.Context(), filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("expected error for missing base dir")
	}
}

func TestRetainNamedPreservesOrderAndDropsUnknown(t *testing.T) {
	repos := []Repo{{Name: "alpha"}, {Name: "beta"}, {Name: "gamma"}}
	got := RetainNamed(repos, []string{"gamma", "alpha", "ghost"})
	names := make([]string, len(got))
	for i, r := range got {
		names[i] = r.Name
	}
	if !reflect.DeepEqual(names, []string{"alpha", "gamma"}) {
		t.Fatalf("names = %v", names)
	}
	if len(RetainNamed(repos, nil)) != 0 {
		t.Fatal("empty selection should yield empty")
	}
}

func TestMissingNamesReportsUnknownInOrder(t *testing.T) {
	all := []Repo{{Name: "alpha"}, {Name: "beta"}, {Name: "gamma"}}
	got := MissingNames(all, []string{"beta", "ghost", "alpha", "phantom"})
	if !reflect.DeepEqual(got, []string{"ghost", "phantom"}) {
		t.Fatalf("missing = %v", got)
	}
	if MissingNames(all, []string{"alpha", "beta"}) != nil {
		t.Fatal("all present should yield no missing")
	}
	if MissingNames(all, nil) != nil {
		t.Fatal("empty want should yield no missing")
	}
}
