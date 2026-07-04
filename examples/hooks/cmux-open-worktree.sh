#!/usr/bin/env sh
#
# cmux-open-worktree.sh
# =============================================================================
# worktree-integrator の `after` フック用サンプルスクリプト。
#
# worktree の作成（または既存への再呼び出し）が終わったあと、**いま操作している
# ターミナル（呼び出し元の cmux サーフェス）自体を** その worktree のルート
# （`WT_ROOT` = <worktrees_dir>/<worktree名>）へ `cd` させます。新しいタブ／
# ワークスペースは開きません。
#
# CLI バイナリは子プロセスなので、呼び出し元シェルの cwd を直接書き換えることは
# できません。代わりに cmux の `send` コマンドでソケット経由で「現在のサーフェスに
# `cd <ルート>` という文字列＋Enter を送り込む」ことで、コマンドが終了して
# プロンプトに戻った瞬間にそのシェルが当該 worktree へ移動します（タイプアヘッド）。
#
# -----------------------------------------------------------------------------
# MCP 実行時の安全性（重要）
# -----------------------------------------------------------------------------
# このフックを MCP サーバー経由で実行した場合、「呼び出し元の対話ターミナル」は
# 存在しません。MCP サーバーは子プロセスの stdin を /dev/null に付け替えており、
# さらに環境変数 `CMUX_SURFACE_ID` はエージェント側の（ユーザーが操作しているのとは
# 別の）ペインを指している可能性があります。ここで無条件に `cd` を送ると、無関係な
# ターミナル（例: エージェントのペイン）へ文字列が入力されてしまい不具合になります。
#
# そこで本スクリプトは「stdin が本物のターミナル（tty）であること」を対話 CLI の
# 目印として使い、その場合だけ `cd` を送ります。MCP 実行時（stdin が /dev/null）は
# 何もせず正常終了（exit 0）するため、誤入力は発生しません。
#
# -----------------------------------------------------------------------------
# 想定する依存
# -----------------------------------------------------------------------------
#   * cmux … ターミナル/ワークスペースアプリ。`cmux` が PATH に無ければ
#            cmux.app のバンドル CLI（CMUX_BUNDLED_CLI_PATH）にフォールバック。
#
#   cmux が見つからない／対話ターミナルでない／cmux 操作に失敗した場合でも、
#   フックは worktree 作成を止めないよう正常終了（exit 0）します。設定側でも
#   `allow_failure = true` を付けておくことを推奨します。
#
# -----------------------------------------------------------------------------
# 設定方法 (~/.config/worktree-integrator.toml)
# -----------------------------------------------------------------------------
#   [[hooks.after]]
#   name          = "cmux-open-worktree"
#   command       = "/path/to/examples/hooks/cmux-open-worktree.sh"
#   allow_failure = true   # cmux 操作に失敗しても worktree 作成は止めない
#
# worktree-integrator は `after` フックに WT_ROOT / WT_WORKTREE_NAME を渡します。
# 詳細は README の「フック（hooks）」を参照してください。
# =============================================================================

set -eu

# --- 設定（環境変数で上書き可能） ------------------------------------------

# cmux CLI の起動コマンド。未指定なら PATH の `cmux`、無ければ cmux.app の
# バンドル CLI（CMUX_BUNDLED_CLI_PATH。cmux ターミナル内では自動設定される）。
# 値は「単一の実行ファイル」（コマンド名または絶対パス）であること。スペースを
# 含むパスは可だが、フラグ等の引数を埋め込むことはできない。
CMUX_BIN=${CMUX_BIN:-}

# --- ユーティリティ ---------------------------------------------------------

log() { printf '[cmux-open-worktree] %s\n' "$*" >&2; }

# 使用する cmux CLI を解決して標準出力に返す。解決できなければ理由をログに出して
# 空文字を返す（呼び出し側はそれを見て遷移をスキップする）。
resolve_cmux() {
	if [ -n "$CMUX_BIN" ]; then
		# CMUX_BIN は単一の実行ファイルのみ（引数の埋め込みは不可）。
		if command -v "$CMUX_BIN" >/dev/null 2>&1; then
			printf '%s' "$CMUX_BIN"
		else
			log "CMUX_BIN を実行できません（単一の実行ファイルを指定してください。引数は埋め込めません）: $CMUX_BIN"
		fi
		return 0
	fi
	if command -v cmux >/dev/null 2>&1; then
		printf '%s' cmux
		return 0
	fi
	if [ -n "${CMUX_BUNDLED_CLI_PATH:-}" ] && [ -x "${CMUX_BUNDLED_CLI_PATH:-}" ]; then
		printf '%s' "$CMUX_BUNDLED_CLI_PATH"
		return 0
	fi
	log "cmux が見つかりません（PATH の cmux も CMUX_BUNDLED_CLI_PATH も利用できません）。遷移をスキップします。"
}

# 「いま操作している cmux サーフェス（＝呼び出し元ターミナル）」に cd を送ってよい
# 状況かどうかを判定し、対象サーフェス ID を標準出力に返す（不可なら空＋非ゼロ）。
#
#   * CLI（対話実行）では、フックは worktree-integrator を起動した端末の
#     stdin/stdout を引き継ぐので stdin が tty になり、$CMUX_SURFACE_ID が
#     その端末のサーフェスを指す。
#   * MCP 実行では、MCP サーバーが stdin を /dev/null に付け替えており（tty でない）、
#     $CMUX_SURFACE_ID も呼び出し元ではないペインを指しうる。→ 送ってはいけない。
caller_surface() {
	# 対話ターミナルでなければ（= MCP 実行など）何もしない。
	[ -t 0 ] || return 1
	[ -n "${CMUX_SURFACE_ID:-}" ] || return 1
	printf '%s' "$CMUX_SURFACE_ID"
}

# 文字列を POSIX シェル用にシングルクォートで安全に括る（埋め込まれた ' は
# '\'' に展開）。`cd` の引数としてそのまま端末へ打ち込むため、スペースや特殊文字を
# 含むパスでも壊れないようにする。
shquote() {
	out=\'
	rest=$1
	# rest に ' が含まれる限り、最初の ' の手前までを足して '\'' を挟む。
	while case $rest in *\'*) true ;; *) false ;; esac; do
		out=$out${rest%%\'*}\'\\\'\'
		rest=${rest#*\'}
	done
	printf '%s' "$out$rest'"
}

# --- メイン -----------------------------------------------------------------

main() {
	target=${WT_ROOT:-}
	if [ -z "$target" ]; then
		log "WT_ROOT が未設定です。何もしません。"
		return 0
	fi
	if [ ! -d "$target" ]; then
		log "worktree ルートが存在しません（${target}）。何もしません。"
		return 0
	fi

	# 対話ターミナルでない（MCP 実行など）場合は、誤って別の端末へ cd を
	# 送らないよう、何もせず終了する。
	surface=$(caller_surface) || {
		log "対話ターミナルではないため遷移をスキップします（MCP 実行などでは現在の端末を cd できません）。"
		return 0
	}

	CMUX=$(resolve_cmux)
	if [ -z "$CMUX" ]; then
		# resolve_cmux が具体的な理由を既にログ出力している。
		return 0
	fi

	# シンボリックリンク等を解決してから cd 先にする。
	target_real=$(cd "$target" 2>/dev/null && pwd -P) || target_real=$target

	# 現在のサーフェスへ `cd <ルート>` ＋ Enter を送る。cmux の `send` は文字列中の
	# `\n` を Enter として解釈するため、末尾に \n を付ける（実際の改行ではなく
	# バックスラッシュ + n の 2 文字を渡す）。
	text="cd $(shquote "$target_real")\n"
	if CMUX_QUIET=1 "$CMUX" send --surface "$surface" -- "$text" >/dev/null 2>&1; then
		log "現在のターミナルを worktree へ移動しました（cd）: $target_real"
	else
		log "現在のターミナルへの cd 送信に失敗しました: $target_real"
	fi
}

main "$@"
