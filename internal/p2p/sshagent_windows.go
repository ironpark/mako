package p2p

import (
	"net"
	"os"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
)

// opensshAgentPipe is the pipe Windows' own OpenSSH agent serves, which
// sets no SSH_AUTH_SOCK.
const opensshAgentPipe = `\.\pipe\openssh-ssh-agent`

// dialSSHAgent connects to the SSH agent: the pipe or socket SSH_AUTH_SOCK
// names, else Windows' own.
func dialSSHAgent() (net.Conn, error) {
	timeout := 2 * time.Second
	sock := os.Getenv("SSH_AUTH_SOCK")
	if strings.HasPrefix(sock, `\.\pipe\`) || strings.HasPrefix(sock, `//./pipe/`) {
		return winio.DialPipe(sock, &timeout)
	}
	if sock != "" {
		// A socket Go reaches; Git for Windows' MSYS one is not, and
		// its user may have added the key to Windows' agent too.
		if conn, err := net.DialTimeout("unix", sock, timeout); err == nil {
			return conn, nil
		}
	}
	return winio.DialPipe(opensshAgentPipe, &timeout)
}
