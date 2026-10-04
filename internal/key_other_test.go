//go:build !unix

package internal

import "testing"

func setupKeyFIFO(t *testing.T, _ string) {
	t.Helper()
	t.Skip("requires Unix FIFO support")
}
