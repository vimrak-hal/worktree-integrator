package action

import (
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/vimrak-hal/worktree-integrator/internal/core/config"
)

// Overrides は、環境、設定ファイル、組み込みのデフォルトより優先される、フロントエンドが
// 明示的に与える設定である。ゼロ値（"" / 0）は「フロントエンドがその値を与えなかった」
// ことを意味し、解決はチェーンの次の段へとフォールスルーする（旧実装の *string / *int
// センチネルは廃止した。空文字列の明示指定は従来から未指定と同義だったため、ポインタで
// 区別する意味が無かった）。
//
//	フラグ > 環境変数 > 設定ファイル > 組み込みのデフォルト
//
// 両方のフロントエンド（cli のフラグパーサと MCP のツール層）が Overrides を構築して
// 以下のコンストラクタに渡すため、優先順位は各フロントエンドではなく1か所にまとまっている。
// 環境変数は os.Getenv の直読みではなく getenv 関数として、ホームディレクトリは
// os.UserHomeDir の直読みではなく home 関数として注入される（main / mcpserver が
// os.Getenv・os.UserHomeDir を渡す）ため、解決処理は純関数でありテストは環境の差し替えを
// 必要としない。
type Overrides struct {
	ReposDir     string
	WorktreesDir string
	Remote       string
	// Concurrency は並列度の上限。0 = 未指定（自動決定）、負値はコンストラクタが拒否する。
	Concurrency int
}

// ReposDir はリポジトリのベースディレクトリを解決する
// （フラグ > WT_REPOS_DIR > 設定ファイル > ~/repositories）。
func ReposDir(override string, file *config.File, getenv func(string) string, home func() (string, error)) (string, error) {
	return resolveDir(override, getenv("WT_REPOS_DIR"), file.ReposDir, "repositories", home)
}

// WorktreesDir はworktreeのベースディレクトリを解決する
// （フラグ > WT_WORKTREES_DIR > 設定ファイル > ~/worktrees）。
func WorktreesDir(override string, file *config.File, getenv func(string) string, home func() (string, error)) (string, error) {
	return resolveDir(override, getenv("WT_WORKTREES_DIR"), file.WorktreesDir, "worktrees", home)
}

// resolveDir はディレクトリ設定の 4 段の優先順位を 1 箇所で適用する。いずれの段も
// 空でない値のみを採用する（空文字列 = 未指定）。デフォルトの ~/<defaultName> は
// home（os.UserHomeDir の注入）がホームディレクトリを特定できない場合にエラーとなる
// （旧実装の「相対パスへの静かなフォールバック」はカレントディレクトリ依存の事故を
// 招くため廃止した）。
func resolveDir(override, env, fileValue, defaultName string, home func() (string, error)) (string, error) {
	if override != "" {
		return override, nil
	}
	if env != "" {
		return env, nil
	}
	if fileValue != "" {
		return fileValue, nil
	}
	dir, err := home()
	if err != nil {
		return "", fmt.Errorf("既定のディレクトリ ~/%s を解決できません（ホームディレクトリを特定できません）: %w", defaultName, err)
	}
	return filepath.Join(dir, defaultName), nil
}

// remote は fetch 元のリモートを解決する
// （フラグ > WT_REMOTE > 設定ファイル > "origin"）。
func remote(override string, file *config.File, getenv func(string) string) string {
	if override != "" {
		return override
	}
	if v := getenv("WT_REMOTE"); v != "" {
		return v
	}
	if file.Remote != "" {
		return file.Remote
	}
	return "origin"
}

// concurrency は並列度の上限を解決する
// （フラグ > WT_CONCURRENCY > 設定ファイル > 0 = 自動決定）。
// どの段でも 0 は「未指定」として次の段へフォールスルーする。負値はエラー。
func concurrency(override int, file *config.File, getenv func(string) string) (int, error) {
	if override < 0 {
		return 0, fmt.Errorf("並列度は 0（自動）以上で指定してください: %d", override)
	}
	if override > 0 {
		return override, nil
	}
	if raw := getenv("WT_CONCURRENCY"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("環境変数 WT_CONCURRENCY の値 %q が不正です（0 以上の整数を指定してください）", raw)
		}
		if n > 0 {
			return n, nil
		}
	}
	if file.Concurrency > 0 {
		return file.Concurrency, nil
	}
	return 0, nil
}

// NewCreate は create ワークフローの設定をフロントエンドのオーバーライド・環境変数
// （getenv 経由）・設定ファイルから解決する。worktree 名と、明示された各リポジトリ名を
// 検証する（リポジトリの実在確認は探索結果を持つ app/create が行う）。base は CLI の
// --base フラグ / MCP の base パラメータで、空文字列は「未指定」（[repos.<name>].base
// → [defaults].base → "auto" へフォールスルー。解決は Create.BaseFor がリポジトリごとに
// 行う）を意味する。
func NewCreate(name string, repos []string, all bool, base string, ov Overrides, file *config.File, getenv func(string) string, home func() (string, error)) (Create, error) {
	parsed, err := ParseName(name)
	if err != nil {
		return Create{}, err
	}
	if len(repos) > 0 && all {
		return Create{}, fmt.Errorf("--repo と --all は同時に指定できません")
	}
	for _, r := range repos {
		if err := validateRepoName(r); err != nil {
			return Create{}, err
		}
	}
	// base・remote は解決後に git fetch の位置引数（ブランチ名・リモート名）としてそのまま
	// 渡るため、"--upload-pack=<cmd>" 等でのオプション混同・任意コマンド実行を型の手前で
	// 塞ぐ。検証の入口はこのコンストラクタに一元化し、CLI も MCP も必ずここを通す。
	// 空の base は「未指定」（BaseFor がリポジトリごとに解決する）を意味するため素通しし、
	// 参照しうる全ソース（フラグ / [defaults].base / [repos.<name>].base）を検証する。
	if base != "" {
		if err := validateBase(base); err != nil {
			return Create{}, err
		}
	}
	if file.Defaults.Base != "" {
		if err := validateBase(file.Defaults.Base); err != nil {
			return Create{}, fmt.Errorf("[defaults].base: %w", err)
		}
	}
	for repoName, rc := range file.Repos {
		if rc.Base != "" {
			if err := validateBase(rc.Base); err != nil {
				return Create{}, fmt.Errorf("[repos.%s].base: %w", repoName, err)
			}
		}
	}
	resolvedRemote := remote(ov.Remote, file, getenv)
	if err := validateRemote(resolvedRemote); err != nil {
		return Create{}, err
	}
	reposDir, err := ReposDir(ov.ReposDir, file, getenv, home)
	if err != nil {
		return Create{}, err
	}
	worktreesDir, err := WorktreesDir(ov.WorktreesDir, file, getenv, home)
	if err != nil {
		return Create{}, err
	}
	conc, err := concurrency(ov.Concurrency, file, getenv)
	if err != nil {
		return Create{}, err
	}
	return Create{
		WorktreeName: parsed,
		Repos:        repos,
		All:          all,
		ReposDir:     reposDir,
		WorktreesDir: worktreesDir,
		Remote:       resolvedRemote,
		// 0 は自動決定。実効的な並列度は選択されるリポジトリ数にも依存するため、
		// 最終決定は worktree.Concurrency が行う。
		Concurrency: conc,
		Base:        base,
		// Hooks・Defaults・RepoConfigs は設定ファイルからのみ取得される。
		Hooks:       file.Hooks,
		Defaults:    file.Defaults,
		RepoConfigs: file.Repos,
	}, nil
}

// NewServerCommand は server コマンドの共通実行コンテキストをフロントエンドの
// オーバーライド・環境変数（getenv 経由）・設定ファイルから解決し、リポジトリ
// フィルタの各名前を検証する。適用されるのはディレクトリのオーバーライドのみで
// ある（server コマンドにはリモートや並列度がない）。操作（ServerKind）は App の
// 型付きメソッドへ別途渡される。
func NewServerCommand(ov Overrides, file *config.File, getenv func(string) string, home func() (string, error), repos []string) (ServerCommand, error) {
	for _, r := range repos {
		if err := validateRepoName(r); err != nil {
			return ServerCommand{}, err
		}
	}
	reposDir, err := ReposDir(ov.ReposDir, file, getenv, home)
	if err != nil {
		return ServerCommand{}, err
	}
	worktreesDir, err := WorktreesDir(ov.WorktreesDir, file, getenv, home)
	if err != nil {
		return ServerCommand{}, err
	}
	return ServerCommand{
		ReposDir:     reposDir,
		WorktreesDir: worktreesDir,
		Servers:      file.ServersConfig(),
		Repos:        repos,
	}, nil
}
