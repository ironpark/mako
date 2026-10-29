package protocol

import "testing"

func TestPlatformNamesTheSystem(t *testing.T) {
	for _, c := range []struct{ os, arch, want string }{
		{"darwin", "arm64", "macOS arm64"},
		{"linux", "amd64", "Linux x86-64"},
		{"windows", "386", "Windows x86"},
		{"plan9", "riscv64", "plan9 riscv64"},
		{"", "", ""},
	} {
		if got := (&Node{Os: c.os, Arch: c.arch}).Platform(); got != c.want {
			t.Errorf("%s/%s: %q, want %q", c.os, c.arch, got, c.want)
		}
	}
}
