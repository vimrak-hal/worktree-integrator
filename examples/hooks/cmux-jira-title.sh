#!/usr/bin/env sh
#
# cmux-jira-title.sh
# =============================================================================
# worktree-integrator の `before` フック用サンプルスクリプト。
#
# worktree 名が Jira チケットの形式（例: ABC-123, PROJ-4567）に一致する場合に、
# Jira MCP サーバーからチケットのタイトルを取得し、現在の cmux タブのタイトルに
# 設定します。あわせて、取得したタイトルを worktree-integrator の別名として
# 登録するため `worktree-integrator alias set <worktree名> <タイトル>` を呼び出し、
# `worktree-integrator server status` の ALIAS 列に表示されるようにします。
# Jira チケット形式でない場合は何もせず正常終了します。
#
# -----------------------------------------------------------------------------
# 想定する依存
# -----------------------------------------------------------------------------
#   * mcptools の `mcp` コマンド (https://github.com/f/mcptools)
#       … Jira MCP サーバーをシェルから呼び出すための汎用 MCP クライアント。
#   * jq … MCP のレスポンス(JSON)からタイトルを取り出すために使用。
#   * Jira MCP サーバー … 環境に合わせて JIRA_MCP_CMD で起動コマンドを指定する。
#
#   いずれも環境に合わせて差し替え可能です。社内 CLI（jira-cli / acli 等）や
#   REST API を使う場合は fetch_jira_title() の中身を置き換えてください。
#
# -----------------------------------------------------------------------------
# 設定方法 (~/.config/worktree-integrator/config.toml)
# -----------------------------------------------------------------------------
#   [[hooks.before]]
#   name          = "cmux-jira-title"
#   command       = "/path/to/examples/hooks/cmux-jira-title.sh"
#   allow_failure = true   # Jira 取得に失敗しても worktree 作成は止めない
#
# worktree-integrator は `before` フックに WT_WORKTREE_NAME を渡します。
# 詳細は README の「フック（hooks）」を参照してください。
# =============================================================================

set -eu

# --- 設定（環境変数で上書き可能） ------------------------------------------

# Jira MCP サーバーの起動コマンド。利用する MCP サーバーに合わせて変更する。
JIRA_MCP_CMD=${JIRA_MCP_CMD:-"npx -y mcp-jira"}

# 呼び出す MCP ツール名と、チケットキーを渡すパラメータ名。
JIRA_MCP_TOOL=${JIRA_MCP_TOOL:-"getJiraIssue"}
JIRA_MCP_KEY_PARAM=${JIRA_MCP_KEY_PARAM:-"issueKey"}

# MCP レスポンス(JSON)からタイトルを取り出す jq フィルタ。
# サーバーが返す JSON 構造に合わせて調整する。
JIRA_TITLE_JQ=${JIRA_TITLE_JQ:-'.fields.summary // .summary // .title // empty'}

# worktree-integrator 本体の起動コマンド（PATH 上の名前、または絶対パス）。
# 別名の登録に使う。PATH に無ければ登録はスキップする。
WTI_BIN=${WTI_BIN:-worktree-integrator}

# Jira チケットキーの正規表現（プロジェクトキー-番号）。
JIRA_KEY_RE='^[A-Za-z][A-Za-z0-9]+-[0-9]+'

# --- ユーティリティ ---------------------------------------------------------

log() { printf '[cmux-jira-title] %s\n' "$*" >&2; }

# コマンドの存在を確認する。無ければ理由をログに出して 1 を返す。
# 第 2 引数に補足（パッケージ名など）を渡すとメッセージに添える。
require_cmd() {
	cmd=$1
	hint=${2:-}
	if command -v "$cmd" >/dev/null 2>&1; then
		return 0
	fi
	if [ -n "$hint" ]; then
		log "$cmd ($hint) が見つかりません。タイトル取得をスキップします。"
	else
		log "$cmd が見つかりません。タイトル取得をスキップします。"
	fi
	return 1
}

# --- Jira ------------------------------------------------------------------

# worktree 名の先頭から Jira チケットキーを抽出する。
# 例: "abc-123-fix-login" -> "ABC-123" / "feature/foo" -> ""（空）
extract_ticket() {
	name=$1
	key=$(printf '%s' "$name" | grep -oE "$JIRA_KEY_RE" || true)
	# プロジェクトキー部分は Jira では大文字なので正規化する。
	printf '%s' "$key" | tr '[:lower:]' '[:upper:]'
}

# Jira MCP サーバーを呼び出し、生レスポンス(JSON)を標準出力に返す。
# mcptools: `mcp call <tool> --params <json> -- <server 起動コマンド>`
call_jira_mcp() {
	ticket=$1
	params=$(printf '{"%s":"%s"}' "$JIRA_MCP_KEY_PARAM" "$ticket")
	# shellcheck disable=SC2086
	mcp call "$JIRA_MCP_TOOL" --params "$params" -- $JIRA_MCP_CMD 2>/dev/null
}

# MCP の生レスポンスからチケットのタイトルを取り出す。
# サーバーが result.content[].text に JSON 文字列を返す前提で本文を取り出し、
# その中から JIRA_TITLE_JQ でタイトルを抽出する（素の JSON にも対応）。
parse_jira_title() {
	jq -r "
		if has(\"content\") then
			(.content[]? | select(.type == \"text\") | .text)
			| (try (fromjson | ($JIRA_TITLE_JQ)) catch .)
		else
			$JIRA_TITLE_JQ
		end
	" 2>/dev/null | head -n 1
}

# Jira チケットのタイトルを取得する。
# 取得できれば標準出力に出し、失敗時は空文字で終了コード 1。
fetch_jira_title() {
	ticket=$1

	require_cmd mcp mcptools || return 1
	require_cmd jq || return 1

	raw=$(call_jira_mcp "$ticket") || {
		log "Jira MCP の呼び出しに失敗しました（$ticket）。"
		return 1
	}

	title=$(printf '%s' "$raw" | parse_jira_title)
	if [ -z "$title" ] || [ "$title" = "null" ]; then
		log "タイトルを抽出できませんでした（$ticket）。"
		return 1
	fi

	printf '%s' "$title"
}

# --- cmux ------------------------------------------------------------------

# OSC 2 エスケープシーケンスでタブ/ウィンドウタイトルを出力する。
emit_osc_title() { printf '\033]2;%s\033\\' "$1"; }

# cmux（端末）のタブタイトルを設定する。
# 制御端末(/dev/tty)へ書き込むことで、フックの標準出力がツールに
# キャプチャされていてもタブに反映される。書けない環境（CI 等）では
# フックを失敗させないよう標準出力へフォールバックする。
set_cmux_tab_title() {
	title=$1
	if { emit_osc_title "$title" >/dev/tty; } 2>/dev/null; then
		return 0
	fi
	emit_osc_title "$title"
}

# --- worktree-integrator 別名 ----------------------------------------------

# 取得した別名を worktree-integrator に登録する。
# state ディレクトリの aliases.toml に保存され、`server status` の ALIAS 列に
# 表示される。キーは（チケットキーではなく）worktree 名そのもの。
# 本体が PATH に無い／登録に失敗しても、フックは失敗させない。
register_alias() {
	value=$1
	name=${WT_WORKTREE_NAME:-}
	[ -n "$name" ] || return 0
	if ! command -v "$WTI_BIN" >/dev/null 2>&1; then
		log "$WTI_BIN が見つかりません。別名の登録をスキップします。"
		return 0
	fi
	if "$WTI_BIN" alias set "$name" "$value" >/dev/null 2>&1; then
		log "別名を登録しました: $name = $value"
	else
		log "別名の登録に失敗しました（$name）。"
	fi
}

# --- メイン -----------------------------------------------------------------

main() {
	name=${WT_WORKTREE_NAME:-}
	if [ -z "$name" ]; then
		log "WT_WORKTREE_NAME が未設定です。何もしません。"
		return 0
	fi

	ticket=$(extract_ticket "$name")
	if [ -z "$ticket" ]; then
		log "worktree 名「$name」は Jira チケット形式ではありません。スキップします。"
		return 0
	fi

	log "Jira チケットを検出: $ticket"

	if title=$(fetch_jira_title "$ticket"); then
		tab_title="$ticket: $title"
		set_cmux_tab_title "$tab_title"
		log "タブタイトルを設定しました: $tab_title"
		register_alias "$tab_title"
	else
		# 取得失敗時はチケットキーだけでもタブ／別名に出しておくと便利。
		set_cmux_tab_title "$ticket"
		log "タイトル取得に失敗したため、チケットキーのみ設定しました: $ticket"
		register_alias "$ticket"
	fi
}

main "$@"
