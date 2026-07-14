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

// CreateInput は NewCreate への入力をまとめた構造体である。フロントエンドが与える
// 生の入力（Name / Repos / All / Base）、優先されるオーバーライド（Overrides）、および
// 解決の材料となる設定ファイルと環境（File / Getenv / Home）を平坦なフィールドで持つ。
// 位置引数の羅列を避け、呼び出し側がフィールド名で意図を明示できるようにする。
type CreateInput struct {
	// Name は作成する worktree（およびブランチ）の未検証の生名（NewCreate が検証する）。
	Name string
	// Repos は --repo / MCP の repos で明示されたリポジトリ名。空かつ All が false なら
	// 対話選択となる。
	Repos []string
	// All はすべての探索済みリポジトリを対象とする（CLI の --all）。
	All bool
	// Base は --base フラグ / MCP の base パラメータ。空は「未指定」を意味する。
	Base string
	// Overrides はフロントエンドが明示的に与える設定（環境・設定ファイル・既定値より
	// 優先される）。
	Overrides Overrides
	// File は読み込み済みの設定ファイル。
	File *config.File
	// Getenv は環境変数の参照（os.Getenv の注入）。
	Getenv func(string) string
	// Home はホームディレクトリの解決（os.UserHomeDir の注入）。
	Home func() (string, error)
}

// NewCreate は create ワークフローの設定をフロントエンドのオーバーライド・環境変数
// （getenv 経由）・設定ファイルから解決する。worktree 名と、明示された各リポジトリ名を
// 検証する（リポジトリの実在確認は探索結果を持つ app/create が行う）。base は CLI の
// --base フラグ / MCP の base パラメータで、空文字列は「未指定」（[repos.<name>].base
// → [defaults].base → "auto" へフォールスルー。解決は Create.BaseFor がリポジトリごとに
// 行う）を意味する。
func NewCreate(in CreateInput) (Create, error) {
	parsed, err := ParseName(in.Name)
	if err != nil {
		return Create{}, err
	}
	if len(in.Repos) > 0 && in.All {
		return Create{}, fmt.Errorf("--repo と --all は同時に指定できません")
	}
	for _, r := range in.Repos {
		if err := validateRepoName(r); err != nil {
			return Create{}, err
		}
	}
	// base・remote は解決後に git fetch の位置引数（ブランチ名・リモート名）としてそのまま
	// 渡るため、"--upload-pack=<cmd>" 等でのオプション混同・任意コマンド実行を型の手前で
	// 塞ぐ。ここで検証するのは「今回の呼び出しで明示・解決されたデフォルト base」だけに
	// 限る: --base フラグと [defaults].base は今回すべてのリポジトリに効く値なので、不正
	// なら即エラーにする。リポジトリ個別の [repos.<name>].base は、今回の create で選択して
	// いないリポジトリの分まで一律に検証すると設定の 1 エントリで全 create が壊れるため、
	// ここでは検証せず、実際に参照される時点（Create.BaseFor）で検証してそのリポジトリ
	// 単位の失敗に留める。空の base は「未指定」（BaseFor が解決する）を意味するため素通しする。
	if in.Base != "" {
		if err := validateBase(in.Base); err != nil {
			return Create{}, err
		}
	}
	if in.File.Defaults.Base != "" {
		if err := validateBase(in.File.Defaults.Base); err != nil {
			return Create{}, fmt.Errorf("[defaults].base: %w", err)
		}
	}
	resolvedRemote := remote(in.Overrides.Remote, in.File, in.Getenv)
	if err := validateRemote(resolvedRemote); err != nil {
		return Create{}, err
	}
	reposDir, err := ReposDir(in.Overrides.ReposDir, in.File, in.Getenv, in.Home)
	if err != nil {
		return Create{}, err
	}
	worktreesDir, err := WorktreesDir(in.Overrides.WorktreesDir, in.File, in.Getenv, in.Home)
	if err != nil {
		return Create{}, err
	}
	conc, err := concurrency(in.Overrides.Concurrency, in.File, in.Getenv)
	if err != nil {
		return Create{}, err
	}
	return Create{
		WorktreeName: parsed,
		Repos:        in.Repos,
		All:          in.All,
		ReposDir:     reposDir,
		WorktreesDir: worktreesDir,
		Remote:       resolvedRemote,
		// 0 は自動決定。実効的な並列度は選択されるリポジトリ数にも依存するため、
		// 最終決定は worktree.Concurrency が行う。
		Concurrency: conc,
		Base:        in.Base,
		// Hooks・Defaults・RepoConfigs は設定ファイルからのみ取得される。
		Hooks:       in.File.Hooks,
		Defaults:    in.File.Defaults,
		RepoConfigs: in.File.Repos,
	}, nil
}

// ServerCommandInput は NewServerCommand への入力をまとめた構造体である。
// フロントエンドが与えるオーバーライドとリポジトリフィルタ、解決の材料となる設定
// ファイルと環境を平坦なフィールドで持つ。旧シグネチャは ov が先頭・repos が末尾と
// 引数順が非対称で NewCreate と揃わなかったため、構造体化して呼び出しの意図を明示する。
type ServerCommandInput struct {
	// Overrides はフロントエンドが明示的に与えるディレクトリのオーバーライド。
	Overrides Overrides
	// File は読み込み済みの設定ファイル。
	File *config.File
	// Getenv は環境変数の参照（os.Getenv の注入）。
	Getenv func(string) string
	// Home はホームディレクトリの解決（os.UserHomeDir の注入）。
	Home func() (string, error)
	// Repos は --repo / MCP の repos による対象リポジトリの絞り込み。空はすべての
	// 設定済みリポジトリを意味する。
	Repos []string
}

// NewServerCommand は server コマンドの共通実行コンテキストをフロントエンドの
// オーバーライド・環境変数（getenv 経由）・設定ファイルから解決し、リポジトリ
// フィルタの各名前を検証する。適用されるのはディレクトリのオーバーライドのみで
// ある（server コマンドにはリモートや並列度がない）。操作（ServerKind）は App の
// 型付きメソッドへ別途渡される。
func NewServerCommand(in ServerCommandInput) (ServerCommand, error) {
	for _, r := range in.Repos {
		if err := validateRepoName(r); err != nil {
			return ServerCommand{}, err
		}
	}
	reposDir, err := ReposDir(in.Overrides.ReposDir, in.File, in.Getenv, in.Home)
	if err != nil {
		return ServerCommand{}, err
	}
	worktreesDir, err := WorktreesDir(in.Overrides.WorktreesDir, in.File, in.Getenv, in.Home)
	if err != nil {
		return ServerCommand{}, err
	}
	return ServerCommand{
		ReposDir:     reposDir,
		WorktreesDir: worktreesDir,
		Servers:      in.File.ServersConfig(),
		Repos:        in.Repos,
	}, nil
}
