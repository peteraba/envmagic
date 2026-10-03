//go:build !unix

package internal

import "os"

// OwnerUID returns the uid that owns info, and false where the platform has none.
func OwnerUID(_ os.FileInfo) (int, bool) {
	return 0, false
}
