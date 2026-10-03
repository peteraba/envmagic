//go:build !unix

package main

import "os"

func ownerUID(_ os.FileInfo) (int, bool) {
	return 0, false
}
