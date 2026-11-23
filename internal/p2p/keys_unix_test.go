//go:build !windows

package p2p

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func TestHostCloseClosesSSHAgentSocket(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: priv}); err != nil {
		t.Fatal(err)
	}
	pub, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		t.Fatal(err)
	}

	sockDir := t.TempDir()
	sockPath := filepath.Join(sockDir, "a.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	var wg sync.WaitGroup
	connClosed := make(chan struct{}, 2)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				defer func() { connClosed <- struct{}{} }()
				_ = agent.ServeAgent(keyring, c)
				_ = c.Close()
			}(c)
		}
	}()

	t.Setenv("SSH_AUTH_SOCK", sockPath)
	pubPath := filepath.Join(t.TempDir(), "id_ed25519.pub")
	if err := os.WriteFile(pubPath, ssh.MarshalAuthorizedKey(pub), 0o600); err != nil {
		t.Fatal(err)
	}

	signer, err := LoadSigner(pubPath)
	if err != nil {
		t.Fatalf("LoadSigner: %v", err)
	}
	h, err := New(context.Background(), Config{
		Dir:   t.TempDir(),
		Host:  "127.0.0.1",
		NoNAT: true,
		Node:  "n",
		Name:  "n",
		Owner: signer,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := h.Close(); err != nil {
		t.Fatalf("Host.Close: %v", err)
	}
	// Agent server connection must close promptly when Host.Close() runs.
	<-connClosed
	if _, err := signer.Sign(rand.Reader, []byte("after-close")); err == nil {
		t.Fatal("expected Sign to fail after Host.Close closed agent socket")
	}
	ln.Close()
	wg.Wait()
}
