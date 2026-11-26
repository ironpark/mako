package main

import (
	"slices"
	"testing"

	"github.com/ironpark/mako/internal/config"
)

func TestProjectsDirsCLIManagement(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("MAKO_HOME", dir)

	if err := projectsDirs([]string{"add", "~/Projects", "/tmp/work"}); err != nil {
		t.Fatalf("add failed: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"~/Projects", "/tmp/work"}; !slices.Equal(cfg.ProjectDirs, want) {
		t.Fatalf("after add: %v, want %v", cfg.ProjectDirs, want)
	}

	if err := projectsDirs([]string{"default", "/tmp/work"}); err != nil {
		t.Fatalf("default failed: %v", err)
	}
	cfg, _ = config.Load()
	if want := []string{"/tmp/work", "~/Projects"}; !slices.Equal(cfg.ProjectDirs, want) {
		t.Fatalf("after default: %v, want %v", cfg.ProjectDirs, want)
	}

	if err := projectsDirs([]string{"remove", "~/Projects"}); err != nil {
		t.Fatalf("remove failed: %v", err)
	}
	cfg, _ = config.Load()
	if want := []string{"/tmp/work"}; !slices.Equal(cfg.ProjectDirs, want) {
		t.Fatalf("after remove: %v, want %v", cfg.ProjectDirs, want)
	}
}
