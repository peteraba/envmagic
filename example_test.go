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

	previous, exists := os.LookupEnv("XDG_CONFIG_HOME")
	defer func() {
		if exists {
			_ = os.Setenv("XDG_CONFIG_HOME", previous)
		} else {
			_ = os.Unsetenv("XDG_CONFIG_HOME")
		}
	}()
	if err := os.Setenv("XDG_CONFIG_HOME", dir); err != nil {
		panic(err)
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
