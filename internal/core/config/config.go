// Package config は、TOML 設定ファイル（スキーマ v2）から任意のオーバーライドを
// 読み込む。
//
// 設定は次の優先順位で解決される
//
//	コマンドラインフラグ > 環境変数 > 設定ファイル > 組み込みデフォルト
//
// このパッケージは、そのチェーンのうち「設定ファイル」の部分を担う。残りの部分は
// cli / action パッケージで接続される。
//
// スキーマ v2 は [repos.<name>] へリポジトリ単位の設定（base ブランチ・copy・
// servers）を集約する（旧 [copy.<repo>] + [servers.<repo>.*] の分散を解消）。
// 旧スキーマとの後方互換は無い: 未知キー拒否がそのまま移行のきっかけになるよう、
// エラーメッセージに新スキーマへの移行ヒントを添える（undecodedMessage を参照）。
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/vimrak-hal/worktree-integrator/internal/core/hooks"
	"github.com/vimrak-hal/worktree-integrator/internal/core/server"
)

// File はスキーマ v2 の TOML 設定ファイルから読み込まれた値を表す。すべての
// スカラーフィールドはゼロ値が「未指定」を意味し（*string の pointer-as-optional は
// 廃止した）、優先順位チェーン（フラグ > 環境変数 > 設定ファイル > 既定値）の次の段へ
// フォールスルーする。
type File struct {
	ReposDir     string `toml:"repos_dir"`
	WorktreesDir string `toml:"worktrees_dir"`
	Remote       string `toml:"remote"`
	// Concurrency は worktree 作成の並列度の上限。0（またはキー省略）は自動決定を
	// 意味する。
	Concurrency int `toml:"concurrency"`
	// Defaults は全リポジトリ共通の既定値（base ブランチ・copy 設定）。
	Defaults Defaults `toml:"defaults"`
	// Hooks はライフサイクルフック（before / after_worktree / after の 3 タイミング）。
	Hooks hooks.Config `toml:"hooks"`
	// Repos はリポジトリ単位の集約設定（base・copy・servers）。旧
	// [copy.<repo>] + [servers.<repo>.*] の分散をここに統合した。
	Repos map[string]RepoConfig `toml:"repos"`
}

// Defaults は [defaults] テーブル。全リポジトリに適用される既定値を持つ。
type Defaults struct {
	// Base はリポジトリが個別の base を指定しない場合の既定ベースブランチ。
	// 省略時（空文字列）は DefaultBase（"auto"）と同義。
	Base string `toml:"base"`
	// Copy は全リポジトリ共通のコピー設定（旧 [copy]."*" 相当）。
	Copy CopySpec `toml:"copy"`
}

// DefaultBase は base 解決の最終フォールバック値。"auto" は、リモートの
// symbolic-ref → main → master の順にベースブランチを自動解決することを意味する
// （internal/core/git.DefaultBranch を参照）。
const DefaultBase = "auto"

// RepoConfig は 1 つのリポジトリの集約設定（[repos.<name>]）。旧 [copy.<repo>] と
// [servers.<repo>.*] を統合したもの。
type RepoConfig struct {
	// Base はこのリポジトリの既定ベースブランチ。省略時は Defaults.Base を継承し、
	// それも省略なら DefaultBase になる（解決順序は action.Create.BaseFor が持つ）。
	Base string `toml:"base"`
	// Copy はこのリポジトリ固有のコピー設定。Defaults.Copy とマージされる
	// （MergeCopy を参照）。
	Copy CopySpec `toml:"copy"`
	// Servers はこのリポジトリの名前付きサーバー定義。
	Servers map[string]server.Spec `toml:"servers"`
}

// ServersConfig は、全リポジトリのサーバー定義を server.Config（旧トップレベル
// [servers.<repo>.<server>] 相当）へ集約する。サーバーが 1 つも無いリポジトリは
// 含まれない。
func (f *File) ServersConfig() server.Config {
	out := server.Config{}
	for _, name := range sortedRepoNames(f.Repos) {
		if servers := f.Repos[name].Servers; len(servers) > 0 {
			out[name] = servers
		}
	}
	return out
}

func sortedRepoNames(repos map[string]RepoConfig) []string {
	return slices.Sorted(maps.Keys(repos))
}

// Load は既定の設定ファイルパス（DefaultPath）から読み込む。ファイルが存在しない
// 場合は空の設定を返す。ファイルが存在するが読み込み・解析・検証できない場合は
// エラーを報告する。
func Load() (*File, error) {
	path, ok := DefaultPath()
	if !ok {
		return &File{}, nil
	}
	return LoadFrom(path)
}

// DefaultPath は既定の設定ファイルパスを返す:
//
//	$XDG_CONFIG_HOME/worktree-integrator/config.toml
//	（XDG_CONFIG_HOME 未設定時は ~/.config/worktree-integrator/config.toml）
//
// os.UserConfigDir() はあえて使わない: darwin ではそれが ~/Library/Application
// Support に解決され、このツールの既存の慣習（README・wiki・
// worktree-integrator-setup スキルが前提とする ~/.config）と乖離するためである。
// ok は、XDG_CONFIG_HOME が未設定でホームディレクトリも特定できない場合にのみ
// false になる。
func DefaultPath() (path string, ok bool) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "worktree-integrator", "config.toml"), true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	return filepath.Join(home, ".config", "worktree-integrator", "config.toml"), true
}

// LoadFrom は、path から設定を読み込み、ファイルが存在しない場合は空として扱う。
func LoadFrom(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &File{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read configuration file %s: %w", path, err)
	}

	var f File
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		return nil, fmt.Errorf("parse configuration file %s: %w", path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf("parse configuration file %s: %s", path, undecodedMessage(undecoded))
	}
	if err := expandPaths(&f); err != nil {
		return nil, fmt.Errorf("parse configuration file %s: %w", path, err)
	}
	if err := validateAll(&f); err != nil {
		return nil, fmt.Errorf("parse configuration file %s: %w", path, err)
	}
	return &f, nil
}

// undecodedMessage は、デコーダが認識できなかったキー群から報告メッセージを組み立てる。
// 旧スキーマ（v1）のキー配置に対応が付くものが 1 つでもあれば、それを優先してスキーマ
// v2 への移行ヒント付きで報告する（ドキュメント順で最初に見つかった未知キーが、
// たまたま無関係なタイプミスであっても、移行が必要な設定ファイルではヒントを
// 埋もれさせない）。該当が無ければ、最初の未知キーを汎用メッセージで報告する。
func undecodedMessage(keys []toml.Key) string {
	for _, key := range keys {
		if hint := migrationHint(key); hint != "" {
			return fmt.Sprintf("unknown key %q%s", key.String(), hint)
		}
	}
	return fmt.Sprintf("unknown key %q", keys[0].String())
}

// migrationHint は、旧スキーマ（v1）から対応が付く未知キーについて、スキーマ v2 への
// 移行方法を案内する追加文を返す。該当しなければ空文字列。
func migrationHint(key toml.Key) string {
	switch {
	case len(key) >= 1 && key[0] == "servers":
		return "。[servers.<repo>.<server>] はスキーマ v2 で廃止されました。[repos.<repo>.servers.<server>] に移動してください"
	case len(key) >= 1 && key[0] == "copy":
		return "。[copy] はスキーマ v2 で廃止されました。共通設定は [defaults.copy] に、リポジトリ別設定は [repos.<repo>.copy] に移動してください"
	case len(key) == 3 && key[0] == "hooks" && key[2] == "background":
		return "。hooks の background は廃止されました。常駐プロセスは [repos.<リポジトリ>.servers] を使ってください"
	default:
		return ""
	}
}

// expandPaths は repos_dir / worktrees_dir の値に対して "~" 展開を行う。
func expandPaths(f *File) error {
	expanded, err := expandTilde(f.ReposDir)
	if err != nil {
		return err
	}
	f.ReposDir = expanded
	expanded, err = expandTilde(f.WorktreesDir)
	if err != nil {
		return err
	}
	f.WorktreesDir = expanded
	return nil
}

// expandTilde は先頭の "~" を展開する。"~"（単体）または "~/..." のみを対象とし、
// "~otheruser" 形式は展開しない（そのまま返す）。
func expandTilde(p string) (string, error) {
	switch {
	case p == "~":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("`~` を展開できません（ホームディレクトリを特定できません）: %w", err)
		}
		return home, nil
	case strings.HasPrefix(p, "~/"):
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("`~` を展開できません（ホームディレクトリを特定できません）: %w", err)
		}
		return filepath.Join(home, strings.TrimPrefix(p, "~/")), nil
	default:
		return p, nil
	}
}

// validateAll は、TOML デコーダ自身がチェックしない必須フィールドの検証を、各所有
// パッケージの Validate へ委譲する。config パッケージ自身は「デコード → 各 Validate
// 呼び出し」の薄い層である。
func validateAll(f *File) error {
	if f.Concurrency < 0 {
		return fmt.Errorf("`concurrency` must be zero (automatic) or positive, got %d", f.Concurrency)
	}
	if err := f.Hooks.Validate(); err != nil {
		return err
	}
	if err := f.Defaults.Copy.Validate(); err != nil {
		return fmt.Errorf("[defaults.copy] %w", err)
	}
	for _, name := range sortedRepoNames(f.Repos) {
		if err := f.Repos[name].Copy.Validate(); err != nil {
			return fmt.Errorf("[repos.%s.copy] %w", name, err)
		}
	}
	return f.ServersConfig().Validate()
}
