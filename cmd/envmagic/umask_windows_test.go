package main

import "testing"

func permissiveUmask(t *testing.T) {
	t.Helper()
	t.Skip("Unix permission bits and umask are not supported on Windows")
}
