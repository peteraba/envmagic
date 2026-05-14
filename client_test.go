package envmagic_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/peteraba/envmagic"
)

func TestClient_Get_ErrNotFound(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	storePath := filepath.Join(t.TempDir(), ".envmagic")

	c, err := envmagic.OpenWithPath(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })

	_, err = c.Get(envmagic.DefaultNamespace, "MISSING_VAR")
	if !errors.Is(err, envmagic.ErrNotFound) {
		t.Fatalf("Get: want ErrNotFound, got %v", err)
	}
}

func TestOpen_usesDotEnvmagicInCwd(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	store := filepath.Join(dir, ".envmagic")

	c0, err := envmagic.OpenWithPath(store)
	if err != nil {
		t.Fatal(err)
	}
	_ = c0.Close()

	t.Chdir(dir)

	c, err := envmagic.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })

	if _, err := c.Get(envmagic.DefaultNamespace, "ANY"); !errors.Is(err, envmagic.ErrNotFound) {
		t.Fatalf("Open cwd store: Get missing: %v", err)
	}
}
