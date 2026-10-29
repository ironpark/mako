package daemon

import (
	"os"
	"testing"
)

// TestMain gives the tests a mako folder of their own: a daemon reads its
// extensions, and compiles them into, the folder MAKO_HOME names, which
// would otherwise be the user's. And a home folder of their own: a daemon
// lets in the keys of the user's ~/.ssh/authorized_keys, and is vouched
// for by the user's SSH key, which would let the tests' daemons in to each
// other and list keys the tests did not.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "mako-daemon-test-")
	if err != nil {
		panic(err)
	}
	user, err := os.MkdirTemp("", "mako-daemon-test-home-")
	if err != nil {
		panic(err)
	}
	os.Setenv("MAKO_HOME", home)
	os.Setenv("HOME", user)
	os.Unsetenv("SSH_AUTH_SOCK")
	code := m.Run()
	os.RemoveAll(home)
	os.RemoveAll(user)
	os.Exit(code)
}
