package config

import (
	"errors"
	"io/fs"
	"os"
)

// CheckStatus は `wt config check` の判定種別（言語非依存）。文言は持たず、表示層
// （adapter/render）がそれぞれの案内と終了コードへ写す。
type CheckStatus int

const (
	// CheckMissing は設定ファイルが存在しないこと（既定値で動作する）を表す。
	CheckMissing CheckStatus = iota
	// CheckOK は設定ファイルが存在し、検証を通過したことを表す。
	CheckOK
	// CheckInvalid は設定ファイルの読み取り・検証に失敗したことを表す（詳細は Err）。
	CheckInvalid
)

// CheckResult は Check の判定結果。パスと種別（と不正時の原因）だけを運ぶ言語非依存の
// 型であり、文言整形は render.ConfigCheck が担う。
type CheckResult struct {
	// Path は判定対象の設定ファイルパス。
	Path string
	// Status は判定種別。
	Status CheckStatus
	// Err は Status=CheckInvalid のときの原因（それ以外は nil）。
	Err error
}

// Check は path の設定ファイルを検証し、言語非依存の判定結果を返す。Load / LoadFrom が
// ファイル不存在を静かに空扱いするのと異なり、`wt config check` は不存在・正常・不正の
// 3 通りを区別して報告する必要があるため、その判定をここに閉じ込める。
func Check(path string) CheckResult {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return CheckResult{Path: path, Status: CheckMissing}
	} else if err != nil {
		return CheckResult{Path: path, Status: CheckInvalid, Err: err}
	}
	if _, err := LoadFrom(path); err != nil {
		return CheckResult{Path: path, Status: CheckInvalid, Err: err}
	}
	return CheckResult{Path: path, Status: CheckOK}
}
