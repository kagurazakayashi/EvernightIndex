//go:build !windows

package app

import "testing"

// holdExclusiveFile 在沒有共享模式概念的平台上不提供此情境。
func holdExclusiveFile(t *testing.T, path string) (func(), bool) {
	t.Helper()
	return nil, false
}
