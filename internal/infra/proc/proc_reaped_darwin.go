//go:build darwin

package proc

// GroupReaped は darwin では常に false を返し、従来の生存判定挙動を維持する。
// ゾンビ（状態 Z）の検出は /proc の走査を前提とするため、この判定は Linux 専用で
// あり、darwin では kill(-pgid, 0) による生存確認のみに委ねる。
func GroupReaped(pgid int) bool {
	return false
}
