package envmagic_test

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/peteraba/envmagic"
	"github.com/peteraba/envmagic/internal"
)

func Example() {
	dir, err := os.MkdirTemp("", "envmagic-example-")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	for _, name := range []string{"XDG_CONFIG_HOME", "HOME", "AppData"} {
		previous, exists := os.LookupEnv(name)
		defer func() {
			if exists {
				_ = os.Setenv(name, previous)
			} else {
				_ = os.Unsetenv(name)
			}
		}()
		if err := os.Setenv(name, dir); err != nil {
			panic(err)
		}
	}

	keyPath, err := internal.KeyPath()
	if err != nil {
		panic(err)
	}
	if rel, err := filepath.Rel(dir, keyPath); err != nil || !filepath.IsLocal(rel) {
		panic(fmt.Sprintf("key path %q is outside example directory %q: %v", keyPath, dir, err))
	}

	if _, _, err := internal.LoadOrCreateKey(); err != nil {
		panic(err)
	}
	storePath := filepath.Join(dir, ".envmagic")
	store, err := internal.OpenStore(storePath)
	if err != nil {
		panic(err)
	}
	if err := store.Close(); err != nil {
		panic(err)
	}

	c, err := envmagic.OpenWithPath(storePath)
	if err != nil {
		panic(err)
	}
	defer func() { _ = c.Close() }()

	names, err := c.Load(envmagic.DefaultNamespace)
	if err != nil {
		panic(err)
	}
	fmt.Println(len(names))
	// Output: 0
}
