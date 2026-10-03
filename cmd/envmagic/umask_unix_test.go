//go:build !windows

package main

import (
	"syscall"
	"testing"
)

func permissiveUmask(t *testing.T) {
	t.Helper()
	previous := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(previous) })
}
