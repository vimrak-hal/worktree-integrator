// Package cli はコマンドラインを Invocation（封印された sum-type）へと解析する。
//
// Parse は I/O を一切行わない純関数である。設定ファイルの読み込み（config.Load）と
// 環境変数の参照（os.Getenv）は main に集約され、解決済みアクションへの変換
// （優先順位「フラグ > 環境変数 > 設定ファイル > 既定値」の適用）は package action の
// コンストラクタが担う。cobra が生成するヘルプ／バージョンのテキストもプロセスの
// stdout へ直接書かず、HelpShown バリアントとして呼び出し元（main）に返す。
package cli

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/vimrak-hal/worktree-integrator/internal/buildinfo"
	"github.com/vimrak-hal/worktree-integrator/internal/core/action"
)

// Invocation はコマンドラインが表す起動要求である。封印された sum-type であり、
// 実装は以下のバリアントのみ。main はこれを網羅的な型スイッチでディスパッチする
// （旧実装の「result==nil なら help 済み」という暗黙推論と ErrHelpShown センチネル、
// runMCP bool 戻り値をこの単一チャネルに置き換えた）。
type Invocation interface {
	isInvocation()
}

// Create は worktree 作成の起動要求。Name は未検証の生文字列で、検証（ParseName）と
// 設定のマージは action.NewCreate が行う。
type Create struct {
	Name string
	// Repos は --repo（繰り返し可）で明示されたリポジトリ名。
	Repos []string
	// All は --all（すべての探索済みリポジトリを対象）。
	All bool
	// Base は --base で明示されたベースブランチ/ref のオーバーライド。空は
	// 「未指定」（[repos.<name>].base → [defaults].base → "auto" へフォールスルー）。
	Base string
	Ov   action.Overrides
}

// List は worktree 一覧の起動要求。
type List struct {
	// Json は Result を JSON で出力する（表示形式の選択であり、action の語彙には
	// 含めない）。
	Json bool
}

// Enter は既存 worktree への遷移（after フックのみ実行）の起動要求。名前は解析の
// 時点で検証済みである。
type Enter struct {
	Name action.Name
}

// Remove は worktree 削除の起動要求。Force は CLI 専用の安全弁の解除であり、MCP の
// worktree_remove には対応するパラメータが存在しない（dirty はエラーを返すのみ）。
type Remove struct {
	Name       action.Name
	Force      bool
	KeepBranch bool
}

// Doctor は自己診断の起動要求。
type Doctor struct {
	// Fix は修復可能な発見をその場で修復する（--fix）。
	Fix  bool
	Json bool
}

// Repos は repos_dir 直下のリポジトリ一覧の起動要求。
type Repos struct{}

// Server は server サブコマンドの起動要求。
type Server struct {
	Kind action.ServerKind
	// Repos は --repo によるリポジトリの絞り込み。
	Repos []string
	Ov    action.Overrides
	// FollowLogs は `server logs -f`（tail -f）。ログの追跡は CLI 専用の表示手段の
	// ため action.LogsKind には存在せず、main が LogsResult のパス情報を受けて
	// 自前で tail -f を実行する（MCP からは型レベルで到達不能）。
	FollowLogs bool
	// Json は `server status --json`（Result を JSON で出力する）。表示形式の選択で
	// あり、action の語彙には含めない。
	Json bool
}

// Alias は alias サブコマンドの起動要求。Kind は解決を要しない完成済みの操作である。
type Alias struct {
	Kind action.AliasKind
}

// RunMCP は MCP サーバーとしての起動要求。ワークフローではなく実行モードである。
type RunMCP struct{}

// RunUI は TUI（端末 UI）としての起動要求。RunMCP と同じくワークフローではなく
// 実行モードだが、設定と状態ルートを必要とするため main のディスパッチは App 構築の
// 後になる（adapter/tui が TUI 専用の子プロセス IO・進捗通知で App を組み直す）。
type RunUI struct{}

// ConfigCheck は `wt config check` の起動要求。設定ファイルの検証結果に応じて
// exit 0/1 を返す（main.runConfigCheck を参照）。App を必要としないため、
// HelpShown / RunMCP と同様に main が dispatch の手前で直接処理する。
type ConfigCheck struct{}

// HelpShown は cobra がヘルプまたはバージョンのテキストを生成したことを表す。
// Text をプロセスの stdout へ書き出すのは呼び出し元（main）の責務である。
type HelpShown struct{ Text string }

func (Create) isInvocation()      {}
func (List) isInvocation()        {}
func (Enter) isInvocation()       {}
func (Remove) isInvocation()      {}
func (Doctor) isInvocation()      {}
func (Repos) isInvocation()       {}
func (Server) isInvocation()      {}
func (Alias) isInvocation()       {}
func (RunMCP) isInvocation()      {}
func (RunUI) isInvocation()       {}
func (ConfigCheck) isInvocation() {}
func (HelpShown) isInvocation()   {}

// Parse は args（プログラム名を除く）を Invocation へと解析する。サブコマンドのない
// 素の `worktree-integrator <name>` の形式は `create <name>` として扱われる。素の名前が
// 予約語（現行および将来のサブコマンド名）と衝突する場合は、`create` の明示を促す
// エラーを返す。
func Parse(args []string) (Invocation, error) {
	// result はサブコマンドが解決するまで nil のまま。正常な Execute の後も nil で
	// あれば、cobra が --help / --version（または未指定サブコマンドのヘルプ）を
	// 処理してテキストを help バッファへ書いたことを意味する。
	var result Invocation

	root := &cobra.Command{
		Use:           "worktree-integrator",
		Short:         "Create Git worktrees across multiple repositories, and switch which worktree's server runs",
		Version:       buildinfo.Version(),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf("worktree 名を指定してください（例: worktree-integrator <name>）")
		},
	}
	// そうしないと Cobra は `completion` サブコマンドを自動登録してしまい、
	// 文字どおり `completion` という名前の worktree を覆い隠してしまう（素の名前の
	// 形式では、サブコマンドでないトークンはすべて create 名として扱われる）。
	root.CompletionOptions.DisableDefaultCmd = true

	// ヘルプ／バージョンの出力はプロセスの stdout ではなくバッファへ取り込み、
	// HelpShown として返す（Parse を純関数に保つ）。
	var help bytes.Buffer
	root.SetOut(&help)
	root.SetErr(&help)

	createCmd := addCreate(root, &result)
	addTree(root, &result)
	addServer(root, &result)
	addAlias(root, &result)
	addMCP(root, &result)
	addUI(root, &result)
	addConfig(root, &result)

	injected, err := injectCreate(args, valueFlags(createCmd))
	if err != nil {
		return nil, err
	}
	root.SetArgs(injected)
	if err := root.Execute(); err != nil {
		return nil, err
	}
	if result == nil {
		return HelpShown{Text: help.String()}, nil
	}
	return result, nil
}

// addDirFlags は解決用ディレクトリのフラグを cmd に定義する。create と server 系
// のみが受け付ける。alias 系や tree 系（list / enter / remove / doctor / repos）は
// フラグを持たない（受理されて黙って無視される経路を作らない。tree 系のディレクトリは
// 設定ファイルと WT_* 環境変数から解決される）。
func addDirFlags(fs *pflag.FlagSet) {
	fs.String("repos-dir", "", "Base directory containing the source repositories (env WT_REPOS_DIR)")
	fs.String("worktrees-dir", "", "Base directory under which worktrees are created (env WT_WORKTREES_DIR)")
}

// dirOverrides は実行されたコマンドからディレクトリの解決用フラグを読み取る。
// フラグ未指定はゼロ値（""）のまま残り、解決が優先順位チェーンの次のリンクへ
// フォールスルーする。
func dirOverrides(cmd *cobra.Command) action.Overrides {
	fs := cmd.Flags()
	var ov action.Overrides
	ov.ReposDir, _ = fs.GetString("repos-dir")
	ov.WorktreesDir, _ = fs.GetString("worktrees-dir")
	return ov
}

func addCreate(root *cobra.Command, result *Invocation) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create Git worktrees across the selected repositories (the default)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			fs := c.Flags()
			repos, _ := fs.GetStringArray("repo")
			all, _ := fs.GetBool("all")
			base, _ := fs.GetString("base")
			ov := dirOverrides(c)
			ov.Remote, _ = fs.GetString("remote")
			ov.Concurrency, _ = fs.GetInt("concurrency")
			*result = Create{Name: args[0], Repos: repos, All: all, Base: base, Ov: ov}
			return nil
		},
	}
	cmd.Flags().StringArray("repo", nil, "Create only in this repository, skipping the interactive prompt (repeatable)")
	cmd.Flags().Bool("all", false, "Create in every discovered repository, skipping the interactive prompt")
	cmd.Flags().String("base", "", "Override the base branch/ref to create from (defaults to repos.<repo>.base / defaults.base / auto-detecting the remote's default branch)")
	addDirFlags(cmd.Flags())
	cmd.Flags().String("remote", "", "Remote to fetch from (env WT_REMOTE; defaults to origin)")
	cmd.Flags().IntP("concurrency", "j", 0, "Maximum repositories processed in parallel (0 = automatic; env WT_CONCURRENCY)")
	root.AddCommand(cmd)
	return cmd
}

// addTree は worktree ライフサイクルの残り半分（list / enter / remove / doctor）と
// repos を登録する。
func addTree(root *cobra.Command, result *Invocation) {
	// 注意: ルート直下のコマンドに別名（ls / rm など）を付けてはならない。素の
	// `<name>` 形式の解析（injectCreate）は knownSubcommand の集合だけを見るため、
	// ここに現れない別名トークンは worktree 名として create に化けてしまう。
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List every worktree with its alias, member repositories and running servers",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			json, _ := c.Flags().GetBool("json")
			*result = List{Json: json}
			return nil
		},
	}
	listCmd.Flags().Bool("json", false, "Print the list as JSON")

	enterCmd := &cobra.Command{
		Use:   "enter <name>",
		Short: "Run the `after` hooks for an existing worktree (e.g. to navigate into it)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name, err := action.ParseName(args[0])
			if err != nil {
				return err
			}
			*result = Enter{Name: name}
			return nil
		},
	}

	removeCmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Stop the worktree's servers, remove its checkouts and branch, and clean state/alias/logs",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			name, err := action.ParseName(args[0])
			if err != nil {
				return err
			}
			force, _ := c.Flags().GetBool("force")
			keepBranch, _ := c.Flags().GetBool("keep-branch")
			*result = Remove{Name: name, Force: force, KeepBranch: keepBranch}
			return nil
		},
	}
	removeCmd.Flags().Bool("force", false, "Remove even when a checkout has uncommitted changes (git worktree remove --force)")
	removeCmd.Flags().Bool("keep-branch", false, "Keep the branch instead of deleting it (git branch -D)")

	doctorCmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose stale state, aliases, logs and git worktree metadata (--fix repairs them)",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			fix, _ := c.Flags().GetBool("fix")
			json, _ := c.Flags().GetBool("json")
			*result = Doctor{Fix: fix, Json: json}
			return nil
		},
	}
	doctorCmd.Flags().Bool("fix", false, "Repair the fixable findings (stale records, orphan logs, prunable metadata)")
	doctorCmd.Flags().Bool("json", false, "Print the findings as JSON")

	reposCmd := &cobra.Command{
		Use:   "repos",
		Short: "List the Git repositories under the repositories directory",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			*result = Repos{}
			return nil
		},
	}

	root.AddCommand(listCmd, enterCmd, removeCmd, doctorCmd, reposCmd)
}

func addServer(root *cobra.Command, result *Invocation) {
	// Args: NoArgs + ヘルプを出すだけの RunE により、未知のサブコマンド（例: 廃止
	// 済みの操作名）はヘルプへのフォールスルーではなく「unknown command」エラーに
	// なる（cobra は Runnable でないコマンドの引数検証を行わないため、Runnable に
	// しておく必要がある）。引数なしの `server` は従来どおりヘルプを表示する。
	server := &cobra.Command{
		Use: "server", Short: "Manage per-repository dev servers", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error { return c.Help() },
	}
	// ディレクトリのフラグは server 系のすべての操作で共通。
	addDirFlags(server.PersistentFlags())

	// set は実行されたコマンドの共通フラグ（--repo とディレクトリ）を読み取り、
	// kind と合わせて Server 起動要求として保存する。
	set := func(c *cobra.Command, kind action.ServerKind) {
		repos, _ := c.Flags().GetStringArray("repo")
		*result = Server{Kind: kind, Repos: repos, Ov: dirOverrides(c)}
	}

	switchCmd := &cobra.Command{
		Use: "switch <name>", Aliases: []string{"activate"},
		Short: "Switch which worktree's server runs (per repository)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			name, err := action.ParseName(args[0])
			if err != nil {
				return err
			}
			requireWorktree, _ := c.Flags().GetBool("require-worktree")
			restart, _ := c.Flags().GetBool("restart")
			set(c, action.SwitchKind{
				Name: name, RequireWorktree: requireWorktree, Restart: restart,
			})
			return nil
		},
	}
	switchCmd.Flags().StringArray("repo", nil, "Limit to these repositories (repeatable)")
	switchCmd.Flags().Bool("require-worktree", false, "Error (instead of skipping) when a repository's worktree is missing")
	switchCmd.Flags().Bool("restart", false, "Restart even if the requested worktree's server is already running")

	statusCmd := &cobra.Command{
		Use: "status", Aliases: []string{"ls"},
		Short: "Show which worktree owns each repository's server",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			set(c, action.StatusKind{})
			if srv, ok := (*result).(Server); ok {
				srv.Json, _ = c.Flags().GetBool("json")
				*result = srv
			}
			return nil
		},
	}
	statusCmd.Flags().StringArray("repo", nil, "Limit to these repositories (repeatable)")
	statusCmd.Flags().Bool("json", false, "Print the status as JSON")

	stopCmd := &cobra.Command{
		Use:   "stop [name]",
		Short: "Stop running servers (omit the name to stop every worktree's)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			scope, err := scopeFromArgs(args)
			if err != nil {
				return err
			}
			set(c, action.StopKind{Scope: scope})
			return nil
		},
	}
	stopCmd.Flags().StringArray("repo", nil, "Limit to these repositories (repeatable)")

	logsCmd := &cobra.Command{
		Use: "logs [name]", Aliases: []string{"log"},
		Short: "View server logs (via tail; omit the name for the running servers')",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			scope, err := scopeFromArgs(args)
			if err != nil {
				return err
			}
			lines, _ := c.Flags().GetInt("lines")
			prev, _ := c.Flags().GetBool("prev")
			set(c, action.LogsKind{Scope: scope, Lines: lines, Prev: prev})
			// -f は action の語彙ではなく CLI の表示手段（Server 起動要求のフラグ）。
			if srv, ok := (*result).(Server); ok {
				srv.FollowLogs, _ = c.Flags().GetBool("follow")
				*result = srv
			}
			return nil
		},
	}
	logsCmd.Flags().StringArray("repo", nil, "Limit to these repositories (repeatable)")
	logsCmd.Flags().BoolP("follow", "f", false, "Follow the logs (tail -f)")
	logsCmd.Flags().IntP("lines", "n", 50, "Number of trailing lines to show")
	logsCmd.Flags().Bool("prev", false, "Show the previous generation of the log (rotated at server start)")

	server.AddCommand(switchCmd, statusCmd, stopCmd, logsCmd)
	root.AddCommand(server)
}

func addAlias(root *cobra.Command, result *Invocation) {
	set := func(kind action.AliasKind) {
		*result = Alias{Kind: kind}
	}

	// Args: NoArgs + ヘルプを出すだけの RunE により、廃止された `alias get` などの
	// 未知の操作は（ヘルプではなく）「unknown command」エラーになる（server 側の
	// コメントを参照）。
	alias := &cobra.Command{
		Use: "alias", Short: "Manage per-worktree display aliases shown in `server status`", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error { return c.Help() },
	}

	setCmd := &cobra.Command{
		Use: "set <name> <label>", Short: "Set (or update) the alias shown for a worktree",
		Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			name, err := action.ParseName(args[0])
			if err != nil {
				return err
			}
			set(action.AliasSet{Name: name, Value: args[1]})
			return nil
		},
	}
	listCmd := &cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "List every worktree alias",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			set(action.AliasList{})
			return nil
		},
	}
	rmCmd := &cobra.Command{
		Use: "remove <name>", Aliases: []string{"rm"}, Short: "Remove a worktree's alias",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name, err := action.ParseName(args[0])
			if err != nil {
				return err
			}
			set(action.AliasRemove{Name: name})
			return nil
		},
	}

	// `alias get` は list に統合され廃止された（意図的な仕様変更）。
	alias.AddCommand(setCmd, listCmd, rmCmd)
	root.AddCommand(alias)
}

// addConfig registers `config check`（唯一の config サブサブコマンド）。設定検証は
// App を必要としないため、main が dispatch の手前で ConfigCheck を直接処理する。
func addConfig(root *cobra.Command, result *Invocation) {
	// Args: NoArgs + ヘルプを出すだけの RunE により、未知の操作は（ヘルプではなく）
	// 「unknown command」エラーになる（server / alias 側と同じ規約）。
	cfg := &cobra.Command{
		Use: "config", Short: "Inspect the configuration file", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error { return c.Help() },
	}
	check := &cobra.Command{
		Use:   "check",
		Short: "Validate the configuration file and exit 0/1 accordingly",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			*result = ConfigCheck{}
			return nil
		},
	}
	cfg.AddCommand(check)
	root.AddCommand(cfg)
}

func addUI(root *cobra.Command, result *Invocation) {
	cmd := &cobra.Command{
		Use:   "ui",
		Short: "Open the interactive terminal UI (server logs, status and worktree switching)",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			// TUI は実行モードであり、解決すべき Action を持たない（表示対象・設定は
			// 実行中にティックごとに再解決される）。
			*result = RunUI{}
			return nil
		},
	}
	root.AddCommand(cmd)
}

func addMCP(root *cobra.Command, result *Invocation) {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Run as an MCP (Model Context Protocol) server over stdio",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			// MCP サーバーはワークフローではなく実行モードであり、解決すべき Action を
			// 持たない（ツール呼び出しごとに設定を新たに解決するため、設定ファイルが
			// 存在しない・不正でも起動できなければならず、ここでは読み込まない）。
			*result = RunMCP{}
			return nil
		},
	}
	root.AddCommand(cmd)
}

// scopeFromArgs は省略可能な位置引数の worktree 名を WorktreeScope に変換する。
// 引数の省略のみが「全 worktree 対象」を意味する。明示的な空文字列は（旧実装の
// ように全件へ正規化せず）不正な名前としてエラーになる — 意図的な仕様変更。
func scopeFromArgs(args []string) (action.WorktreeScope, error) {
	if len(args) == 0 {
		return action.AllWorktrees{}, nil
	}
	name, err := action.ParseName(args[0])
	if err != nil {
		return nil, err
	}
	return action.OneWorktree{Name: name}, nil
}

// knownSubcommand は、（素の worktree 名ではなく）実際のサブコマンドである先頭トークンの
// 集合。Cobra のデフォルトの `completion` コマンドは無効化されているため（Parse を参照）、
// `completion` は他のトークンと同様に worktree 名として扱われる。サブコマンド名と同名の
// worktree は `create <name>` の明示で作成できる。
var knownSubcommand = map[string]bool{
	"create": true, "list": true, "enter": true, "remove": true, "doctor": true,
	"repos": true, "server": true, "alias": true, "mcp": true, "ui": true,
	"config": true, "help": true,
}

// reservedSubcommand は、まだ実装されていないが将来のサブコマンドとして予約されている
// 先頭トークンの集合。素の名前として黙って worktree を作ると、コマンドが実装された
// 時点で同じ入力が別の動作に変わってしまうため、予約してエラーで案内する。現在は
// すべて実装済み（knownSubcommand へ移動済み）で空である。将来サブコマンドを追加する
// 際は、実装より先にここへ足すこと。
var reservedSubcommand = map[string]bool{}

// valueFlags は create コマンドのフラグ定義から、（引数を分離する形式で）後続の
// 引数を消費するトークンの集合を導出する。素の名前の形式ではすべての引数が create の
// ものになるため、create のフラグ集合が唯一の情報源である（旧実装の手書きマップは
// フラグ定義との二重管理だった）。`--flag=value` の形式は 1 トークンで完結するため
// この集合には現れない。
func valueFlags(create *cobra.Command) map[string]bool {
	m := map[string]bool{}
	create.Flags().VisitAll(func(f *pflag.Flag) {
		if f.Value.Type() == "bool" {
			return // bool フラグは値を消費しない
		}
		m["--"+f.Name] = true
		if f.Shorthand != "" {
			m["-"+f.Shorthand] = true
		}
	})
	return m
}

// injectCreate は素の `<name>` 形式を `create <name> ...` に書き換える。最初の
// 位置引数のトークンが既知のサブコマンドであればそのまま通し、予約語（将来の
// サブコマンド名）であればエラーで `create` の明示を案内する。それ以外なら先頭に
// "create" を付加する（素の形式ではすべてのフラグが create のローカルフラグであり、
// cobra がルートで解釈できないため、途中挿入ではなく先頭付加とする）。
// `--` 以降は位置引数として解釈しない。
func injectCreate(args []string, valueFlag map[string]bool) ([]string, error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		if strings.HasPrefix(a, "-") {
			if valueFlag[a] && i+1 < len(args) {
				i++ // フラグの値をスキップする
			}
			continue
		}
		// 最初の位置引数トークン。
		if knownSubcommand[a] {
			return args, nil
		}
		if reservedSubcommand[a] {
			return nil, fmt.Errorf(
				"worktree 名 %q はサブコマンド名と衝突します。`worktree-integrator create %s` と明示してください", a, a)
		}
		return append([]string{"create"}, args...), nil
	}
	return args, nil
}
