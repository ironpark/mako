package ahp

import (
	"os"
	"path/filepath"
	"testing"
)

// The endpoint listens on loopback alone, and its token is made once, for
// its owner alone.
func TestEndpointIsLocal(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:8787", "[::1]:8787", "localhost:0"} {
		if err := loopback(addr); err != nil {
			t.Errorf("%s: %v", addr, err)
		}
	}
	for _, addr := range []string{"0.0.0.0:8787", ":8787", "192.168.1.2:8787", "example.com:80", "nonsense"} {
		if loopback(addr) == nil {
			t.Errorf("%s was taken as loopback", addr)
		}
	}
	path := filepath.Join(t.TempDir(), "ahp-token")
	first, err := Token(path)
	if err != nil || len(first) != 64 {
		t.Fatalf("Token = %q, %v", first, err)
	}
	if again, _ := Token(path); again != first {
		t.Fatal("the token changed")
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("token mode: %v, %v", fi.Mode(), err)
	}
}
