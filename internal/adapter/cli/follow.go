package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

// FollowLogs は `tail -f` を実行してログファイル群を追跡する CLI 専用の表示手段で
// ある。追跡対象のパスはワークフロー（App.ServerLogs）の LogsResult から取り出し、
// main が渡す。フォローは action の語彙に存在しないため、MCP からこの経路へは
// 型レベルで到達できない（stdio を JSON-RPC が占有する MCP で tail -f が戻らず
// ハングする事故の根絶）。
//
// tail -f は戻ってこないため、ctx のキャンセル（Ctrl-C）で終了させる。その際の
// 非ゼロ終了（ExitError）は失敗ではなく通常の終わり方として扱う。
func FollowLogs(ctx context.Context, paths []string, lines int) error {
	args := []string{"-f", "-n", strconv.Itoa(max(lines, 0))}
	args = append(args, paths...)
	c := exec.CommandContext(ctx, "tail", args...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return fmt.Errorf("`tail -f` を実行できません: %w", err)
		}
	}
	return nil
}
