package daemon

import (
	"os"
	"testing"
)

// TestMain gives the tests a mako folder of their own: a daemon reads its
// extensions, and compiles them into, the folder MAKO_HOME names, which
// would otherwise be the user's.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "mako-daemon-test-")
	if err != nil {
		panic(err)
	}
	os.Setenv("MAKO_HOME", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}
