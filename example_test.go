package envmagic_test

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/peteraba/envmagic"
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

	c, err := envmagic.OpenWithPath(filepath.Join(dir, ".envmagic"))
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
