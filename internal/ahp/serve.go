package ahp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gosuda/go-ahp/jsonrpc"
	"github.com/gosuda/go-ahp/wstransport"
)

type ahpTransport = jsonrpc.Transport

// Path is where the endpoint takes WebSocket connections.
const Path = "/ahp"

// Endpoint is where a Bridge is served.
type Endpoint struct {
	// Listen is a loopback host:port for WebSocket, with Token.
	Listen string
	// Socket is a Unix socket path for WebSocket without a token: its mode,
	// 0600, keeps other users out. "" for none.
	Socket string
	// Token is the loopback endpoint's secret.
	Token string
	// Origins are the browser origins, besides the endpoint's own, that
	// may connect.
	Origins []string
}

// Serve serves b on e until ctx ends.
func Serve(ctx context.Context, b *Bridge, e Endpoint) error {
	if err := loopback(e.Listen); err != nil {
		return err
	}
	if e.Token == "" {
		return errors.New("ahp: the loopback endpoint needs a token")
	}
	withToken, err := wstransport.NewHandler(b.ServeConn, wstransport.Options{Token: e.Token, OriginPatterns: e.Origins, PingInterval: 30 * time.Second})
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", e.Listen)
	if err != nil {
		return err
	}
	servers := []*http.Server{serveOn(ln, withToken)}
	if e.Socket != "" {
		open, err := wstransport.NewHandler(b.ServeConn, wstransport.Options{AllowAnonymous: true, PingInterval: 30 * time.Second})
		if err != nil {
			return err
		}
		_ = os.Remove(e.Socket)
		uln, err := net.Listen("unix", e.Socket)
		if err != nil {
			servers[0].Close()
			return err
		}
		if err := os.Chmod(e.Socket, 0o600); err != nil {
			uln.Close()
			servers[0].Close()
			return err
		}
		servers = append(servers, serveOn(uln, open))
	}
	<-ctx.Done()
	for _, s := range servers {
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = s.Shutdown(sctx)
		cancel()
	}
	if e.Socket != "" {
		_ = os.Remove(e.Socket)
	}
	return nil
}

func serveOn(ln net.Listener, h http.Handler) *http.Server {
	mux := http.NewServeMux()
	mux.Handle(Path, h)
	s := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = s.Serve(ln) }()
	return s
}

// loopback refuses an address that is not this machine's own: remote
// clients come through SSH or a tunnel.
func loopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("ahp: listen %q: %w", addr, err)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("ahp: listen %q is not a loopback address", addr)
}

// Token is the endpoint's secret kept at path, made with mode 0600 on the
// first call.
func Token(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil {
		if t := strings.TrimSpace(string(b)); t != "" {
			return t, nil
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	t := hex.EncodeToString(raw)
	if err := os.WriteFile(path, []byte(t+"\n"), 0o600); err != nil {
		return "", err
	}
	return t, nil
}
