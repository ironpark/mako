//go:build !windows

package p2p

import (
	"errors"
	"net"
	"os"
	"time"
)

// dialSSHAgent connects to the SSH agent SSH_AUTH_SOCK names.
func dialSSHAgent() (net.Conn, error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, errors.New("no SSH agent")
	}
	return net.DialTimeout("unix", sock, 2*time.Second)
}
