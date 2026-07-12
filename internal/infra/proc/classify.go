package proc

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
)

// ResultKind は子プロセスの終了を分類した結果を表す封印列挙である。os/exec は
// キャンセルや期限切れで殺したプロセスも *exec.ExitError（"signal: killed"）として
// 報告するため、err の型を見るだけでは「中断」と「異常終了」を区別できない。その
// 判定を Classify に一箇所へ集約し、その結果をこの型で表す。
type ResultKind int

const (
	// ResultOK: 正常終了（exit 0）。
	ResultOK ResultKind = iota
	// ResultCanceled: 親 ctx のキャンセルにより中断・強制終了された。
	ResultCanceled
	// ResultTimedOut: per-job の期限（child ctx の DeadlineExceeded）超過により
	// 強制終了された。
	ResultTimedOut
	// ResultExitNonZero: 実行はされたが 0 以外の終了コードで終わった。
	ResultExitNonZero
	// ResultStartFailed: プロセスをそもそも起動・実行できなかった。
	ResultStartFailed
)

// String は列挙値の識別名を返す（ログ・テストの可読性のため）。
func (k ResultKind) String() string {
	switch k {
	case ResultOK:
		return "OK"
	case ResultCanceled:
		return "Canceled"
	case ResultTimedOut:
		return "TimedOut"
	case ResultExitNonZero:
		return "ExitNonZero"
	case ResultStartFailed:
		return "StartFailed"
	default:
		return fmt.Sprintf("ResultKind(%d)", int(k))
	}
}

// Classify は子プロセスの実行結果 err を、ctx の状態と突き合わせて分類する。
// キャンセルや期限切れで殺されたプロセスも *exec.ExitError（"signal: killed"）と
// して報告されるため、単に err の型を見るだけでは「中断」と「異常終了」を区別
// できない。そこで「ctx を先に見る」という判定を、core/git・procctl・core/hooks が
// 個別に持っていたのをここへ一本化する。
//
// parent は実行全体のキャンセルを表す ctx、child は per-job の期限付き ctx である
// （呼び出し側に per-job の期限が無ければ parent と同じものを渡す）。判定順は
// 親のキャンセル → child の期限超過 → *exec.ExitError（コード抽出）→ err==nil の
// 正常終了 → それ以外の起動失敗。返す exitCode は ResultExitNonZero のときのみ
// 意味を持ち（それ以外は 0）、シグナルで殺された等でコードを取得できない場合は
// *exec.ExitError の報告どおり -1 になる。
//
// 呼び出し側が「正常終了（err==nil）か否か」を先に振り分ける既存の骨格
// （例: `if err != nil { … }`）を保てば、コマンド成功の直後に親がキャンセルされた
// 競合でも、親キャンセルの判定に先んじて成功が優先される。
func Classify(parent, child context.Context, err error) (kind ResultKind, exitCode int) {
	if parent.Err() != nil {
		return ResultCanceled, 0
	}
	if errors.Is(child.Err(), context.DeadlineExceeded) {
		return ResultTimedOut, 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return ResultExitNonZero, exit.ExitCode()
	}
	if err == nil {
		return ResultOK, 0
	}
	return ResultStartFailed, 0
}
