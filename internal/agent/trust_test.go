package agent

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestCanonicalDirsResolvesSymlinksAndDeduplicates(t *testing.T) {
	base := t.TempDir()
	realA := filepath.Join(base, "real-a")
	realB := filepath.Join(base, "real-b")
	if err := os.MkdirAll(realA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(realB, 0o755); err != nil {
		t.Fatal(err)
	}
	linkA := filepath.Join(base, "link-a")
	if err := os.Symlink(realA, linkA); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	wantA, _ := filepath.EvalSymlinks(realA)
	wantB, _ := filepath.EvalSymlinks(realB)
	got := CanonicalDirs([]string{"", "  ", linkA, realA + string(filepath.Separator), realB, linkA})
	want := []string{filepath.Clean(wantA), filepath.Clean(wantB)}
	if !slices.Equal(got, want) {
		t.Fatalf("CanonicalDirs = %q, want %q", got, want)
	}
}

func TestFlagsExtractDirs(t *testing.T) {
	base := t.TempDir()
	dirA := filepath.Join(base, "sub-a")
	dirB := filepath.Join(base, "sub-b")
	if err := os.MkdirAll(dirA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dirB, 0o755); err != nil {
		t.Fatal(err)
	}

	// Variadic --add-dir (Claude style): stops before a trailing prompt that is not a directory.
	variadic := &Flags{
		Values:   []string{"--system-prompt", "--model"},
		Variadic: []string{"--add-dir"},
	}
	got := variadic.ExtractDirs([]string{
		"claude",
		"--system-prompt", "--add-dir", // value of --system-prompt, not a flag
		"--add-dir=" + dirA,
		"--add-dir", "sub-b", "fix the tests",
		"--", "--add-dir", dirA,
	}, base)
	want := []string{dirA, dirB}
	if !slices.Equal(got, want) {
		t.Fatalf("variadic ExtractDirs = %q, want %q", got, want)
	}

	// Value --add-dir + -C/--cd (Codex style).
	value := &Flags{
		Values: []string{"--add-dir", "-C", "--cd", "--model"},
	}
	got = value.ExtractDirs([]string{
		"codex",
		"--model", "--add-dir", // value of --model
		"-C", dirA,
		"--add-dir", "sub-b",
		"--cd=" + dirB,
	}, base)
	want = []string{dirA, dirB, dirB}
	if !slices.Equal(got, want) {
		t.Fatalf("value ExtractDirs = %q, want %q", got, want)
	}
}

func TestLookupEnvHonorsExplicitEmptyOverride(t *testing.T) {
	t.Setenv("MAKO_TEST_TRUST_ENV", "/from-process")
	if got := LookupEnv(nil, "MAKO_TEST_TRUST_ENV"); got != "/from-process" {
		t.Fatalf("nil env: got %q", got)
	}
	if got := LookupEnv(map[string]string{"MAKO_TEST_TRUST_ENV": "/from-launch"}, "MAKO_TEST_TRUST_ENV"); got != "/from-launch" {
		t.Fatalf("override: got %q", got)
	}
	if got := LookupEnv(map[string]string{"MAKO_TEST_TRUST_ENV": ""}, "MAKO_TEST_TRUST_ENV"); got != "" {
		t.Fatalf("explicit empty override should shadow process env, got %q", got)
	}
}
