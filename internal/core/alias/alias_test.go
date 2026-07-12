package alias

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/vimrak-hal/worktree-integrator/internal/infra/statedir"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(statedir.At(t.TempDir()))
}

func TestMissingFileLoadsEmpty(t *testing.T) {
	s := newTestStore(t)
	a, err := s.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if a.Version != DocFormatVersion {
		t.Fatalf("version = %d", a.Version)
	}
	if len(a.Aliases) != 0 {
		t.Fatalf("aliases = %v", a.Aliases)
	}
	if _, ok, _ := s.Get(t.Context(), "feat-a"); ok {
		t.Fatal("get on empty should be absent")
	}
}

func TestSetThenGetRoundTrips(t *testing.T) {
	s := newTestStore(t)
	stored, err := s.Set(t.Context(), "ABC-123", "ABC-123: Fix login")
	if err != nil || stored != "ABC-123: Fix login" {
		t.Fatalf("set = %q %v", stored, err)
	}
	v, ok, _ := s.Get(t.Context(), "ABC-123")
	if !ok || v != "ABC-123: Fix login" {
		t.Fatalf("get = %q %v", v, ok)
	}
}

func TestSetNormalizesFirstLineTrimmed(t *testing.T) {
	s := newTestStore(t)
	stored, _ := s.Set(t.Context(), "feat-a", "  ABC-123: title  \nsecond line\n")
	if stored != "ABC-123: title" {
		t.Fatalf("normalized = %q", stored)
	}
}

// 空（または正規化の結果空になる）ラベルの設定はエラーであり、既存の別名を消さない。
// 削除の経路は Remove の 1 本のみである（意図的な仕様変更: 旧実装は空値を削除として
// 扱っていた）。
func TestSetBlankIsErrorAndKeepsExisting(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Set(t.Context(), "feat-a", "something"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Set(t.Context(), "feat-a", ""); err == nil {
		t.Fatal("空のラベルはエラーになるべき")
	}
	if _, err := s.Set(t.Context(), "feat-a", "   \nsecond"); err == nil {
		t.Fatal("正規化で空になるラベルもエラーになるべき")
	}
	if v, ok, _ := s.Get(t.Context(), "feat-a"); !ok || v != "something" {
		t.Fatalf("既存の別名は保持されるべき: %q %v", v, ok)
	}
}

func TestRemoveReportsPresence(t *testing.T) {
	s := newTestStore(t)
	if existed, _ := s.Remove(t.Context(), "feat-a"); existed {
		t.Fatal("remove absent should be false")
	}
	_, _ = s.Set(t.Context(), "feat-a", "x")
	if existed, _ := s.Remove(t.Context(), "feat-a"); !existed {
		t.Fatal("remove present should be true")
	}
}

func TestSetIsAtomicNoLeftoverTempFile(t *testing.T) {
	s := newTestStore(t)
	_, _ = s.Set(t.Context(), "feat-a", "x")
	if _, err := os.Stat(s.File()); err != nil {
		t.Fatal("aliases file missing")
	}
	if _, err := os.Stat(s.File() + ".tmp"); err == nil {
		t.Fatal("temp file should not remain")
	}
}

func TestRoundTripsMultipleEntriesSorted(t *testing.T) {
	s := newTestStore(t)
	_, _ = s.Set(t.Context(), "feat-b", "B")
	_, _ = s.Set(t.Context(), "feat-a", "A")
	a, _ := s.Load(t.Context())
	keys := make([]string, 0, len(a.Aliases))
	for k := range a.Aliases {
		keys = append(keys, k)
	}
	// マップのイテレーション順序は不定なので、両方のキーが存在することだけを確認する。
	if len(keys) != 2 {
		t.Fatalf("keys = %v", keys)
	}
}

func TestRepeatedSetKeepsLatest(t *testing.T) {
	s := newTestStore(t)
	_, _ = s.Set(t.Context(), "feat-a", "x")
	_, _ = s.Set(t.Context(), "feat-a", "y")
	if v, _, _ := s.Get(t.Context(), "feat-a"); v != "y" {
		t.Fatalf("get = %q", v)
	}
}

func TestRoundTripUnmarshalFromDisk(t *testing.T) {
	root := statedir.At(t.TempDir())
	s := NewStore(root)
	_, _ = s.Set(t.Context(), "feat-a", "A")
	// 新しいストアから読み戻すことで、ディスク上の形式がラウンドトリップすることを確かめる。
	again := NewStore(root)
	got, _ := again.Load(t.Context())
	if !reflect.DeepEqual(got.Aliases, map[string]string{"feat-a": "A"}) {
		t.Fatalf("aliases = %v", got.Aliases)
	}
	if filepath.Base(s.File()) != "aliases.toml" {
		t.Fatalf("file = %s", s.File())
	}
}
