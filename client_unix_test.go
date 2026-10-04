//go:build unix

package envmagic_test

import (
	"os"
	"strings"
	"testing"

	"github.com/peteraba/envmagic"
	"github.com/peteraba/envmagic/internal"
)

func TestOpenWithPathRejectsRootOwnedFile(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("requires a non-root process")
	}
	isolateKeyPath(t)
	if _, _, err := internal.LoadOrCreateKey(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/etc/hostname", "/etc/hosts", "/etc/passwd"} {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if uid, known := internal.OwnerUID(info); !known || uid != 0 {
			continue
		}
		client, err := envmagic.OpenWithPath(path)
		if client != nil {
			_ = client.Close()
			t.Fatal("OpenWithPath returned a root-owned store")
		}
		if err == nil || !strings.Contains(err.Error(), "owned by uid 0") {
			t.Fatalf("OpenWithPath: err=%v, want root ownership error", err)
		}
		return
	}
	t.Skip("no root-owned file available")
}
