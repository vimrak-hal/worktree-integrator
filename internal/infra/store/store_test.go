package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// docVersion はテスト用ドキュメントのフォーマットバージョン。store.New に明示的に
// 渡すストアごとのバージョンで、テストが期待する値をここへ集約する。
const docVersion uint32 = 1

type doc struct {
	Version uint32            `toml:"version"`
	Entries map[string]string `toml:"entries"`
}

// DocVersion は Versioned を実装し、Load 時のバージョン検証を有効にする。
func (d *doc) DocVersion() uint32 { return d.Version }

func newStore(dir string) *File[doc] {
	return New(filepath.Join(dir, "doc.toml"), "doc", docVersion, func() *doc {
		return &doc{Version: docVersion, Entries: map[string]string{}}
	})
}

// loadVia は共有セッションでドキュメントを読み込むテストヘルパー。
func loadVia(t *testing.T, s *File[doc]) *doc {
	t.Helper()
	session, err := s.Shared(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	d, err := session.Load()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// saveVia は排他セッションでドキュメントを書き込むテストヘルパー。
func saveVia(t *testing.T, s *File[doc], d *doc) {
	t.Helper()
	session, err := s.Exclusive(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	if err := session.Save(d); err != nil {
		t.Fatal(err)
	}
}

func TestMissingFileLoadsDefault(t *testing.T) {
	s := newStore(t.TempDir())
	d := loadVia(t, s)
	if d.Version != docVersion {
		t.Fatalf("version = %d", d.Version)
	}
	if len(d.Entries) != 0 {
		t.Fatalf("entries = %v", d.Entries)
	}
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	s := newStore(t.TempDir())
	saveVia(t, s, &doc{Version: docVersion, Entries: map[string]string{"a": "1"}})
	loaded := loadVia(t, s)
	if loaded.Entries["a"] != "1" {
		t.Fatalf("entries = %v", loaded.Entries)
	}
}

func TestSaveIsAtomicNoLeftoverTempFile(t *testing.T) {
	dir := t.TempDir()
	s := newStore(dir)
	saveVia(t, s, &doc{Version: docVersion})
	if _, err := os.Stat(s.Path()); err != nil {
		t.Fatal("doc file missing")
	}
	if _, err := os.Stat(filepath.Join(dir, "doc.toml.tmp")); err == nil {
		t.Fatal("temp file should not remain")
	}
}

func TestUpdatePersistsOnlyWhenDirty(t *testing.T) {
	s := newStore(t.TempDir())
	if err := s.Update(t.Context(), func(d *doc) (bool, error) {
		d.Entries["k"] = "v"
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	if loaded := loadVia(t, s); loaded.Entries["k"] != "v" {
		t.Fatal("dirty update should persist")
	}

	if err := s.Update(t.Context(), func(d *doc) (bool, error) {
		d.Entries["ignored"] = "x"
		return false, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadVia(t, s).Entries["ignored"]; ok {
		t.Fatal("non-dirty update should not persist")
	}
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	s := newStore(dir)
	if err := os.WriteFile(s.Path(), []byte("nope = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	session, err := s.Shared(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	if _, err := session.Load(); err == nil {
		t.Fatal("expected error for unknown key")
	}
}

// ストアに宣言されたバージョン（docVersion）より新しいバージョンのドキュメントは、
// 黙って読み込んで次の保存で破壊してしまう代わりに、Load でエラーとして拒否される。
func TestLoadRejectsNewerVersion(t *testing.T) {
	s := newStore(t.TempDir())
	saveVia(t, s, &doc{Version: docVersion + 1, Entries: map[string]string{}})

	session, err := s.Shared(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	if _, err := session.Load(); err == nil {
		t.Fatal("expected error for a newer document version")
	}
}

// セッションを Close した後は、同じストアで再びロックを取得できる。
func TestLockReentrantAfterClose(t *testing.T) {
	s := newStore(t.TempDir())
	s1, err := s.Exclusive(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_ = s1.Close()
	s2, err := s.Exclusive(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_ = s2.Close()
}

// 共有（読み取り専用）セッションでの Save は ErrReadOnly で拒否される。
func TestSharedSessionSaveIsReadOnly(t *testing.T) {
	s := newStore(t.TempDir())
	session, err := s.Shared(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	err = session.Save(&doc{Version: docVersion})
	if !errors.Is(err, ErrReadOnly) {
		t.Fatalf("Save on shared session = %v, want ErrReadOnly", err)
	}
	if _, statErr := os.Stat(s.Path()); statErr == nil {
		t.Fatal("shared session must not write the document")
	}
}

// 共有セッション同士は同時に保持できる（LOCK_SH は共存する）。
func TestSharedSessionsCoexist(t *testing.T) {
	s := newStore(t.TempDir())
	s1, err := s.Shared(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s1.Close() }()
	s2, err := s.Shared(t.Context())
	if err != nil {
		t.Fatalf("second shared session should coexist: %v", err)
	}
	_ = s2.Close()
}

// 排他ロックの競合は、タイムアウト（テストでは短縮）後に ErrBusy として報告される。
func TestExclusiveContentionTimesOutWithErrBusy(t *testing.T) {
	s := newStore(t.TempDir())
	s.lockTimeout = 150 * time.Millisecond

	holder, err := s.Exclusive(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Close() }()

	// flock はファイルディスクリプタ単位なので、同一プロセス内でも別のセッションは
	// 競合する。
	start := time.Now()
	_, err = s.Exclusive(t.Context())
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("contended Exclusive = %v, want ErrBusy", err)
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("returned too quickly (%v); expected to retry until the timeout", elapsed)
	}
}

// ロック待ちの間に ctx がキャンセルされると、タイムアウトを待たず ctx.Err() で返る。
func TestExclusiveContentionHonorsContextCancel(t *testing.T) {
	s := newStore(t.TempDir())

	holder, err := s.Exclusive(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Close() }()

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	_, err = s.Exclusive(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled Exclusive = %v, want context.DeadlineExceeded", err)
	}
}

// キャンセル済みの ctx では、無競合でも即座に ctx.Err() で失敗する。
func TestAcquireRejectsCanceledContext(t *testing.T) {
	s := newStore(t.TempDir())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.Exclusive(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Exclusive(canceled) = %v, want context.Canceled", err)
	}
	if err := s.Update(ctx, func(*doc) (bool, error) { return false, nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("Update(canceled) = %v, want context.Canceled", err)
	}
}

// View は読み取り専用で、渡した関数のエラーを伝播する。
func TestViewIsReadOnlyAndPropagatesError(t *testing.T) {
	s := newStore(t.TempDir())
	if err := s.View(t.Context(), func(d *doc) error {
		d.Entries["mutated"] = "x" // View 内での変更は捨てられる
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Path()); err == nil {
		t.Fatal("View must not persist anything")
	}

	sentinel := errors.New("read failed")
	if err := s.View(t.Context(), func(*doc) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("View error = %v, want %v", err, sentinel)
	}
}

// ストアの親ディレクトリの位置に通常ファイルがあると MkdirAll が失敗し、
// セッション取得（ひいては Update）がエラーを返す。
func TestEnsureDirFailsWhenDirIsFile(t *testing.T) {
	parent := t.TempDir()
	file := filepath.Join(parent, "occupied")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 通常ファイルの配下にはディレクトリを作れない。
	s := newStore(filepath.Join(file, "sub"))

	if _, err := s.Exclusive(t.Context()); err == nil {
		t.Fatal("Exclusive should fail when the parent is a regular file")
	}
	if err := s.Update(t.Context(), func(*doc) (bool, error) { return false, nil }); err == nil {
		t.Fatal("Update should fail when the lock cannot be created")
	}
}

// Update は mutate が返したエラーをそのまま伝播し、永続化を行わない。
func TestUpdatePropagatesMutateError(t *testing.T) {
	s := newStore(t.TempDir())
	sentinel := errors.New("boom")
	err := s.Update(t.Context(), func(d *doc) (bool, error) {
		d.Entries["k"] = "v"
		return true, sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Update error = %v, want %v", err, sentinel)
	}
	// mutate がエラーを返したので保存されていないはず。
	if _, statErr := os.Stat(s.Path()); statErr == nil {
		t.Fatal("doc file should not be written when mutate errors")
	}
}

// Update は Load が失敗（不正な TOML）した場合にエラーを返す。
func TestUpdateFailsWhenLoadFails(t *testing.T) {
	s := newStore(t.TempDir())
	if err := os.WriteFile(s.Path(), []byte("not = [valid\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	called := false
	err := s.Update(t.Context(), func(*doc) (bool, error) {
		called = true
		return true, nil
	})
	if err == nil {
		t.Fatal("Update should fail when Load fails")
	}
	if called {
		t.Fatal("mutate should not be called when Load fails")
	}
}
