// Package alias はワークツリーの表示用エイリアスを永続化する。ワークツリー名を
// 人間に分かりやすいラベル（例: Jira チケットのタイトル）に対応づける小さな
// キーバリューストアで、`server status` の ALIAS 列に表示される。
//
// キーがワークツリー名であるだけで、依存上は worktree 作成処理（git/worktree の
// Process / Run）には一切結合しない。infra/store の上に乗る状態ストアであり、
// server サブシステムと状態ルート（statedir.Root）を共有する peer として core 直下に
// 置かれる。その共有は呼び出し側（app 層）が同じ Root を両ストアへ注入することで
// 明示される。
//
// エイリアスは Root 直下の単一のファイル aliases.toml に格納される（server
// サブシステムの servers.toml と同じディレクトリ）。永続化 — アドバイザリロックと、
// 一時ファイル作成＋リネームによるアトミックな書き込み — は共有の store
// プリミティブが提供する。
package alias

import (
	"context"
	"errors"
	"strings"

	"github.com/vimrak-hal/worktree-integrator/internal/infra/statedir"
	"github.com/vimrak-hal/worktree-integrator/internal/infra/store"
)

// DocFormatVersion は aliases.toml のオンディスクフォーマットバージョン。server
// 状態（v2）とは独立して進化するため、alias 側で宣言する。
const DocFormatVersion uint32 = 1

// Aliases は永続化された全エイリアスで、ワークツリー名をキーとする。TOML の
// シリアライズではフィールドの順序が重要で、スカラーの version は aliases の
// サブテーブルより前に出力される。
type Aliases struct {
	Version uint32            `toml:"version"`
	Aliases map[string]string `toml:"aliases"`
}

// DocVersion は store.Versioned を実装する。Load 時に、このビルドが理解できるより
// 新しいフォーマットのファイルを拒否するために使われる。
func (a *Aliases) DocVersion() uint32 { return a.Version }

// Store は状態ルートの下で、アドバイザリロックのもと Aliases を読み書きする。
type Store struct {
	inner *store.File[Aliases]
}

// NewStore は root を状態ルートとするエイリアスストアを返す。
func NewStore(root statedir.Root) *Store {
	inner := store.New(root.AliasesFile(), "aliases", DocFormatVersion, func() *Aliases {
		return &Aliases{Version: DocFormatVersion, Aliases: map[string]string{}}
	})
	return &Store{inner: inner}
}

// File はエイリアスファイルのパス（テストで使用）。
func (s *Store) File() string { return s.inner.Path() }

// Load はエイリアスを読み込む。ファイルが無い場合は空集合として扱う。
// 共有ロックの下で読み取り、返されるドキュメントは呼び出し側だけのコピーである。
func (s *Store) Load(ctx context.Context) (*Aliases, error) {
	var loaded *Aliases
	err := s.inner.View(ctx, func(a *Aliases) error {
		loaded = a
		return nil
	})
	if err != nil {
		return nil, err
	}
	return loaded, nil
}

// Get は worktree にエイリアスが設定されていれば、それを返す。
func (s *Store) Get(ctx context.Context, worktree string) (string, bool, error) {
	a, err := s.Load(ctx)
	if err != nil {
		return "", false, err
	}
	v, ok := a.Aliases[worktree]
	return v, ok, nil
}

// Set はロックのもとで worktree のエイリアスを設定し、保存された値を返す。
//
// 値は最初の 1 行に切り詰めてトリムされるため、複数行や余白を含むラベルが
// ステータステーブルを乱すことはない。正規化の結果が空になる値はエラーとして
// 拒否する。削除の経路は Remove の 1 本のみである（かつては空値を削除として
// 扱っていたが、「設定」と「削除」が同じ操作に同居する暗黙の分岐を仕様ごと
// 刈り込んだ）。
func (s *Store) Set(ctx context.Context, worktree, value string) (stored string, err error) {
	normalized := firstLineTrimmed(value)
	if normalized == "" {
		return "", errors.New("空のラベルは設定できません。削除は alias rm を使ってください")
	}
	err = s.inner.Update(ctx, func(a *Aliases) (bool, error) {
		if a.Aliases == nil {
			a.Aliases = map[string]string{}
		}
		a.Aliases[worktree] = normalized
		return true, nil
	})
	if err != nil {
		return "", err
	}
	return normalized, nil
}

// Remove は worktree のエイリアスを削除し、存在していたかを返す（存在した場合のみ
// 永続化する）。
func (s *Store) Remove(ctx context.Context, worktree string) (existed bool, err error) {
	err = s.inner.Update(ctx, func(a *Aliases) (bool, error) {
		_, existed = a.Aliases[worktree]
		if existed {
			delete(a.Aliases, worktree)
		}
		return existed, nil
	})
	return existed, err
}

// firstLineTrimmed は value を最初の 1 行に切り詰め、前後の空白をトリムする。
func firstLineTrimmed(value string) string {
	if i := strings.IndexByte(value, '\n'); i >= 0 {
		value = value[:i]
	}
	value = strings.TrimSuffix(value, "\r")
	return strings.TrimSpace(value)
}
