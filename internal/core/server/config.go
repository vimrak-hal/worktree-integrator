// Package server はリポジトリごとの dev サーバーを管理する: どのワークツリーの
// サーバーを稼働させるかの切り替えに加え、status / stop / logs を扱う。
//
// 1 つのリポジトリは複数の名前付きサーバー（例: モノレポのバックエンドとフロントエンド）を
// 宣言でき、切り替えはサーバーごとに、以前のワークツリーのインスタンスを停止し、
// ライフサイクルコマンドを実行し、選択したワークツリーで再び起動する。「どのワーク
// ツリーがアクティブか」はサーバーごとに稼働インスタンスの属性（Instance.Worktree）
// から導出され、リポジトリ単位の独立した状態としては持たない。そのため部分失敗時は
// 「backend は旧ワークツリーで稼働継続、frontend は新ワークツリーで稼働」という
// 現実がそのまま状態と status に現れる。
package server

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/vimrak-hal/worktree-integrator/internal/core/cmdspec"
)

// DefaultStopGraceSecs は、サーバーを停止する際の SIGTERM と SIGKILL の間の
// デフォルトの猶予期間で、Spec が独自の値を設定していないときに使われる。
const DefaultStopGraceSecs uint64 = 5

// Config は、リポジトリのディレクトリ名をキーとした、設定済みの全サーバー。
// ネストした [servers.<repo>.<server>] テーブルとして書き出される。
type Config map[string]RepoServers

// GetRepo は repo に設定されたサーバーがあれば返す。
func (s Config) GetRepo(repo string) (RepoServers, bool) {
	r, ok := s[repo]
	return r, ok
}

// IsEmpty は、サーバーが 1 つも設定されていないかどうかを報告する。
func (s Config) IsEmpty() bool {
	for _, servers := range s {
		if len(servers) > 0 {
			return false
		}
	}
	return true
}

// SortedRepoNames は、設定済みのリポジトリ名をソート済みの順序で返す。
func (s Config) SortedRepoNames() []string {
	return slices.Sorted(maps.Keys(s))
}

// Validate は、TOML デコーダ自身がチェックしない必須フィールドを強制する: 全
// リポジトリ・全サーバーについて Spec.Validate を呼び出す。決定論的な順序
// （SortedRepoNames → SortedServerNames）で走査し、最初に見つかったエラーを
// [repos.<repo>.servers.<server>] のコンテキストとともに返す。config パッケージが
// 「デコード → 各 Validate 呼び出し」の薄い層になるよう、検証は所有パッケージである
// ここに置く。
func (s Config) Validate() error {
	for _, repo := range s.SortedRepoNames() {
		servers := s[repo]
		for _, name := range servers.SortedServerNames() {
			if err := servers[name].Validate(); err != nil {
				return fmt.Errorf("server [repos.%s.servers.%s] %w", repo, name, err)
			}
		}
	}
	return nil
}

// RepoServers は、1 つのリポジトリの名前付きサーバー群。
type RepoServers map[string]Spec

// SortedServerNames は、このリポジトリのサーバー名をソート済みの順序で返す。
func (rs RepoServers) SortedServerNames() []string {
	return slices.Sorted(maps.Keys(rs))
}

// Spec は、1 つのサーバーの定義。
//
// start は長時間稼働するサーバー（デタッチして起動される）。ライフサイクルコマンドは
// サーバーの起動直前に、サーバーの作業ディレクトリで完了まで実行される:
//
//   - setup       — ワークツリーが初めてアクティベートされたときのみ。
//   - on_activate — 初回・以降を問わず、すべてのアクティベーションで。
//   - on_switch   — すでに初期化済みのワークツリーを再アクティベートするときのみ。
type Spec struct {
	// Start は長時間稼働するサーバーのコマンドで、デタッチして起動される。
	Start cmdspec.Commands `toml:"start"`
	// Dir はこのサーバーを実行するワークツリーのサブディレクトリ。空の場合、
	// サーバーはリポジトリのワークツリールートで実行される。
	Dir string `toml:"dir"`
	// Setup はワークツリーの初回アクティベーション時にのみ実行される。
	Setup *cmdspec.Commands `toml:"setup"`
	// OnActivate は start の直前、すべてのアクティベーションで実行される。
	OnActivate *cmdspec.Commands `toml:"on_activate"`
	// OnSwitch はすでに初期化済みのワークツリーへ切り替えるときにのみ実行される。
	OnSwitch *cmdspec.Commands `toml:"on_switch"`
	// StopGraceSecs は、このサーバーを停止する際に SIGTERM の後 SIGKILL へ
	// エスカレートするまで待つ時間（デフォルトは DefaultStopGraceSecs）。
	StopGraceSecs *uint64 `toml:"stop_grace_secs"`
}

// Validate はこのサーバー定義が必須フィールドを備えているかを検証する: start
// コマンドは必須である。
func (spec Spec) Validate() error {
	if spec.Start.IsEmpty() {
		return errors.New("is missing its `start` command")
	}
	return nil
}

// Grace は、設定された（またはデフォルトの）SIGTERM → SIGKILL の猶予期間。
func (spec Spec) Grace() time.Duration {
	secs := DefaultStopGraceSecs
	if spec.StopGraceSecs != nil {
		secs = *spec.StopGraceSecs
	}
	return time.Duration(secs) * time.Second
}
