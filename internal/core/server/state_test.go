package server_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vimrak-hal/worktree-integrator/internal/core/server"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/statedir"
)

// TestStateRepoAutoCreates は、Repo が未知のリポジトリに対して空のエントリを生成し、
// 以降の呼び出しが同じエントリを返すことを確認する。
func TestStateRepoAutoCreates(t *testing.T) {
	state := &server.State{} // Repos は nil

	rs := state.Repo("rails")
	if rs == nil {
		t.Fatal("Repo should auto-create an entry")
	}
	if rs.Servers == nil {
		t.Fatal("auto-created RepoState should have a non-nil Servers map")
	}

	// 同じ名前は同じポインタを返す（重複生成しない）。
	rs.Server("backend").LastLog = "/x.log"
	if again := state.Repo("rails"); again != rs {
		t.Fatal("Repo should return the existing entry on the second call")
	}
	if again := state.Repo("rails"); again.Servers["backend"].LastLog != "/x.log" {
		t.Fatal("existing runtime lost")
	}
}

// TestRepoStateServerAutoCreates は、Server が未知のサーバーに対して空の Runtime を
// 生成し、再呼び出しで同じものを返すことを確認する。
func TestRepoStateServerAutoCreates(t *testing.T) {
	rs := &server.RepoState{} // Servers は nil

	rt := rs.Server("backend")
	if rt == nil {
		t.Fatal("Server should auto-create a Runtime")
	}
	if rt.Running != nil || len(rt.Setup) != 0 || rt.LastLog != "" {
		t.Fatalf("new Runtime should be empty, got %+v", rt)
	}

	rt.RecordSetup("feat-a", "/worktrees/feat-a/app")
	if again := rs.Server("backend"); again != rt {
		t.Fatal("Server should return the existing Runtime on the second call")
	}
	if again := rs.Server("backend"); len(again.Setup) != 1 {
		t.Fatalf("existing Setup lost: %v", again.Setup)
	}
}

// TestRecordSetup は、RecordSetup が nil マップを実体化してパスと完了時刻を記録する
// ことを確認する。
func TestRecordSetup(t *testing.T) {
	rt := &server.Runtime{}
	rt.RecordSetup("feat-a", "/worktrees/feat-a/app")
	rec, ok := rt.Setup["feat-a"]
	if !ok {
		t.Fatal("RecordSetup should create the entry")
	}
	if rec.Path != "/worktrees/feat-a/app" {
		t.Fatalf("path = %q", rec.Path)
	}
	if rec.DoneAt == 0 {
		t.Fatal("DoneAt should be set")
	}
}

// TestStateStorePathsFollowRoot は、StateFile と LogsDir が状態ルートから導出される
// ことを確認する。
func TestStateStorePathsFollowRoot(t *testing.T) {
	dir := t.TempDir()
	store := server.NewStateStore(statedir.At(dir))
	if store.StateFile() != filepath.Join(dir, "servers.toml") {
		t.Fatalf("StateFile() = %q", store.StateFile())
	}
	if store.LogsDir() != filepath.Join(dir, "logs") {
		t.Fatalf("LogsDir() = %q", store.LogsDir())
	}
}

// TestLogPathIsInjective は、ログファイル名の単射性を固定する: 旧実装の '/'→'-'
// 平坦化で衝突していた "a/b" と "a-b"、区切りの "__" と衝突していた "a_b"・"a__b" が
// すべて異なるファイルへ写ること、および (repo, server, worktree) の境界の曖昧さが
// 無いことを確認する。
func TestLogPathIsInjective(t *testing.T) {
	store := server.NewStateStore(statedir.At(t.TempDir()))

	// 同一 repo/server の下で、まぎらわしい worktree 名はすべて別ファイルになる。
	seen := map[string]string{}
	for _, wt := range []string{"a/b", "a-b", "a_b", "a__b", "a%2Fb", "a%b"} {
		p := store.LogPath("rails", "backend", wt)
		if prev, ok := seen[p]; ok {
			t.Fatalf("worktree %q and %q collide at %s", wt, prev, p)
		}
		seen[p] = wt
		if filepath.Dir(p) != store.LogsDir() {
			t.Fatalf("log for %q must stay directly under logs/: %s", wt, p)
		}
	}

	// コンポーネント境界の曖昧さ: (a__b, c) と (a, b__c) は別ファイル。
	if store.LogPath("a__b", "c", "wt") == store.LogPath("a", "b__c", "wt") {
		t.Fatal("component boundary must be unambiguous")
	}

	// 素直な名前はそのまま読めるファイル名になる。
	plain := store.LogPath("rails", "backend", "feat-a")
	if !strings.HasSuffix(plain, filepath.Join("logs", "rails__backend__feat-a.log")) {
		t.Fatalf("plain log path = %s", plain)
	}
}

// TestPrevLogPath は、1 世代前のログパスの導出を固定する。
func TestPrevLogPath(t *testing.T) {
	if got := server.PrevLogPath("/logs/a__b__c.log"); got != "/logs/a__b__c.log.prev" {
		t.Fatalf("PrevLogPath = %q", got)
	}
}

// TestStateStoreReadsDoNotCreateLogsDir は、Update / View（status を見るだけの経路）が
// logs/ ディレクトリを作らないことを確認する。ログディレクトリの作成はプロセス起動
// 経路（SpawnDetached）だけが担う。
func TestStateStoreReadsDoNotCreateLogsDir(t *testing.T) {
	dir := t.TempDir()
	store := server.NewStateStore(statedir.At(dir))

	if err := store.View(t.Context(), func(*server.State) error { return nil }); err != nil {
		t.Fatalf("View: %v", err)
	}
	if err := store.Update(t.Context(), func(*server.State) (bool, error) { return false, nil }); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := os.Stat(store.LogsDir()); !os.IsNotExist(err) {
		t.Fatalf("logs dir must not be created by reads, err=%v", err)
	}
}

// TestStateStoreUpdatePersistsWhenDirty は、Update が dirty=true のときだけ永続化することを確認する。
func TestStateStoreUpdatePersistsWhenDirty(t *testing.T) {
	store := server.NewStateStore(statedir.At(t.TempDir()))

	// dirty=false の変更は永続化されない。
	err := store.Update(t.Context(), func(s *server.State) (bool, error) {
		s.Repo("app").Server("backend").LastLog = "/ignored.log"
		return false, nil
	})
	if err != nil {
		t.Fatalf("Update(clean): %v", err)
	}
	if _, err := os.Stat(store.StateFile()); !os.IsNotExist(err) {
		t.Fatalf("state file should not exist after a clean update, err=%v", err)
	}

	// dirty=true は永続化される。
	err = store.Update(t.Context(), func(s *server.State) (bool, error) {
		s.Repo("app").Server("backend").LastLog = "/kept.log"
		return true, nil
	})
	if err != nil {
		t.Fatalf("Update(dirty): %v", err)
	}

	loaded := loadState(t, store)
	repo := loaded.Repos["app"]
	if repo == nil || repo.Servers["backend"] == nil || repo.Servers["backend"].LastLog != "/kept.log" {
		t.Fatalf("dirty update not persisted: %+v", repo)
	}
}

// TestStateStoreUpdatePropagatesError は、mutate のエラーが Update から伝播し、永続化されないことを確認する。
func TestStateStoreUpdatePropagatesError(t *testing.T) {
	store := server.NewStateStore(statedir.At(t.TempDir()))
	sentinel := errors.New("boom")

	err := store.Update(t.Context(), func(s *server.State) (bool, error) {
		s.Repo("app").Server("backend").LastLog = "/x.log"
		return true, sentinel // dirty でもエラーなら保存しない
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Update error = %v, want %v", err, sentinel)
	}
	if _, err := os.Stat(store.StateFile()); !os.IsNotExist(err) {
		t.Fatalf("state file should not exist after a failed update, err=%v", err)
	}
}

// TestStateStoreViewIsReadOnly は、View が変更を永続化しないことを確認する。
func TestStateStoreViewIsReadOnly(t *testing.T) {
	store := server.NewStateStore(statedir.At(t.TempDir()))

	var seenVersion uint32
	err := store.View(t.Context(), func(s *server.State) error {
		seenVersion = s.Version
		s.Repo("app").Server("backend").LastLog = "/mutated.log" // View 内での変更は捨てられる
		return nil
	})
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if seenVersion != server.StateVersion {
		t.Fatalf("View should see the default (factory) version %d, got %d", server.StateVersion, seenVersion)
	}
	if _, err := os.Stat(store.StateFile()); !os.IsNotExist(err) {
		t.Fatalf("View must not persist anything, err=%v", err)
	}
}

// TestStateStoreViewPropagatesError は、View に渡した関数のエラーが伝播することを確認する。
func TestStateStoreViewPropagatesError(t *testing.T) {
	store := server.NewStateStore(statedir.At(t.TempDir()))
	sentinel := errors.New("read failed")
	if err := store.View(t.Context(), func(*server.State) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("View error = %v, want %v", err, sentinel)
	}
}

// legacyV1State は v1 形式（active / initialized を含む）の状態ファイルの内容。
const legacyV1State = `version = 1

[repos.app]
active = "feat-a"

[repos.app.servers.backend]
initialized = ["feat-a"]

[repos.app.servers.backend.running]
pid = 4242
pgid = 4242
log = "/x.log"
started_at = 123
`

// TestLegacyStateMovedAside は、旧形式（v1）の状態ファイルが最初のアクセスで .bak へ
// 退避され（OnLegacy 通知つき）、以降は新規の空状態として動くことを確認する。
// マイグレーションは行わない（意図的な仕様）。
func TestLegacyStateMovedAside(t *testing.T) {
	dir := t.TempDir()
	store := server.NewStateStore(statedir.At(dir))
	if err := os.WriteFile(store.StateFile(), []byte(legacyV1State), 0o644); err != nil {
		t.Fatal(err)
	}
	var notified string
	store.OnLegacy = func(bak string) { notified = bak }

	loaded := loadState(t, store)
	if len(loaded.Repos) != 0 {
		t.Fatalf("legacy content must not be migrated: %+v", loaded.Repos)
	}
	bak := store.StateFile() + ".bak"
	if notified != bak {
		t.Fatalf("OnLegacy = %q, want %q", notified, bak)
	}
	data, err := os.ReadFile(bak)
	if err != nil {
		t.Fatalf("backup should exist: %v", err)
	}
	if string(data) != legacyV1State {
		t.Fatal("backup should preserve the original bytes")
	}
	if _, err := os.Stat(store.StateFile()); !os.IsNotExist(err) {
		t.Fatalf("original state file should be gone, err=%v", err)
	}

	// 2 回目のアクセスでは何も起きない（.bak を上書きしない）。
	notified = ""
	_ = loadState(t, store)
	if notified != "" {
		t.Fatalf("OnLegacy must fire only once, got %q", notified)
	}
}

// TestCurrentVersionStateIsNotTouched は、v2 の状態ファイルが退避されないことを確認する。
func TestCurrentVersionStateIsNotTouched(t *testing.T) {
	store := newStore(t)
	saveState(t, store, &server.State{Version: server.StateVersion, Repos: map[string]*server.RepoState{
		"app": {Servers: map[string]*server.Runtime{"backend": {LastLog: "/x.log"}}},
	}})
	var notified string
	store.OnLegacy = func(bak string) { notified = bak }

	loaded := loadState(t, store)
	if notified != "" {
		t.Fatalf("a current-version file must not be moved aside, got %q", notified)
	}
	if loaded.Repos["app"].Servers["backend"].LastLog != "/x.log" {
		t.Fatal("content should be readable")
	}
}

// TestNewerVersionStateIsRejected は、このビルドより新しいバージョンのファイルが
// （退避ではなく）エラーとして拒否されることを確認する。黙って読み込んで次の保存で
// 破壊する経路を塞ぐ。
func TestNewerVersionStateIsRejected(t *testing.T) {
	dir := t.TempDir()
	store := server.NewStateStore(statedir.At(dir))
	if err := os.WriteFile(store.StateFile(), []byte("version = 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := store.View(t.Context(), func(*server.State) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "より新しい") {
		t.Fatalf("View of a newer-version file = %v, want version rejection", err)
	}
	if _, statErr := os.Stat(store.StateFile()); statErr != nil {
		t.Fatal("a newer-version file must not be moved aside")
	}
}
