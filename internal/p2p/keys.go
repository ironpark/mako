package p2p

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// Who a host lets in is a matter of keys, written as OpenSSH writes them.
// Every host has a key of its own, the one its peer ID names, and may be
// vouched for by an SSH key of its owner's: a certificate, signed with
// that key, that the peer ID is the owner's for a while. A host lets in a
// peer whose own key, or whose owner's, is in its authorized_keys file, or
// whose owner is its own.

// AuthorizedKeysFile holds the keys a host lets in, in its configuration
// directory, in the format of OpenSSH's authorized_keys.
const AuthorizedKeysFile = "authorized_keys"

// Keys are the keys a host lets in, read again from their file when it
// changes.
type Keys struct {
	path string

	mu     sync.Mutex
	loaded bool
	stat   fileStamp
	keys   []AuthorizedKey
	set    map[string]bool // the keys, in SSH wire format
}

// AuthorizedKey is one key in the file.
type AuthorizedKey struct {
	Key     ssh.PublicKey
	Comment string
}

// fileStamp tells a file changed.
type fileStamp struct {
	mod  time.Time
	size int64
}

// NewKeys reads the keys of the authorized_keys file in dir, which need not
// exist yet.
func NewKeys(dir string) *Keys {
	k := &Keys{path: filepath.Join(dir, AuthorizedKeysFile)}
	k.reload()
	return k
}

// reload reads the file again if it changed, as edited by hand, and
// reports whether it did.
func (k *Keys) reload() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.read()
}

// read reads the file if it changed since it was last read, and reports
// whether it did. k.mu is held.
func (k *Keys) read() bool {
	var stamp fileStamp
	if info, err := os.Stat(k.path); err == nil {
		stamp = fileStamp{info.ModTime(), info.Size()}
	}
	if k.loaded && stamp == k.stat {
		return false
	}
	k.loaded, k.stat = true, stamp
	data, _ := os.ReadFile(k.path)
	k.keys = parseAuthorizedKeys(data)
	k.set = make(map[string]bool, len(k.keys))
	for _, a := range k.keys {
		k.set[string(a.Key.Marshal())] = true
	}
	return true
}

// parseAuthorizedKeys reads the keys of an authorized_keys file, leaving
// out lines it cannot read and those whose options restrict the key to a
// command, which a mako link cannot honour.
func parseAuthorizedKeys(data []byte) []AuthorizedKey {
	var keys []AuthorizedKey
	for len(data) > 0 {
		key, comment, options, rest, err := ssh.ParseAuthorizedKey(data)
		if err != nil {
			break
		}
		data = rest
		if restricted(options) {
			continue
		}
		keys = append(keys, AuthorizedKey{Key: key, Comment: comment})
	}
	return keys
}

func restricted(options []string) bool {
	for _, o := range options {
		if strings.HasPrefix(o, "command=") || o == "restrict" {
			return true
		}
	}
	return false
}

// Has reports whether key is let in.
func (k *Keys) Has(key ssh.PublicKey) bool {
	if key == nil {
		return false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.set[string(key.Marshal())]
}

// List is the keys let in.
func (k *Keys) List() []AuthorizedKey {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]AuthorizedKey(nil), k.keys...)
}

// KeyLine is key as an authorized_keys line has it, with comment on one
// line.
func KeyLine(key ssh.PublicKey, comment string) string {
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
	if comment = strings.Join(strings.Fields(comment), " "); comment != "" {
		line += " " + comment
	}
	return line
}

// Add lets key in, with comment, unless it is already.
func (k *Keys) Add(key ssh.PublicKey, comment string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.read()
	if k.set[string(key.Marshal())] {
		return nil
	}
	line := KeyLine(key, comment)
	if err := os.MkdirAll(filepath.Dir(k.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(k.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	// A file whose last line has no end gets one first.
	if info, err := f.Stat(); err == nil && info.Size() > 0 {
		last := make([]byte, 1)
		if r, err := os.Open(k.path); err == nil {
			_, _ = r.ReadAt(last, info.Size()-1)
			r.Close()
		}
		if last[0] != '\n' {
			line = "\n" + line
		}
	}
	_, err = f.WriteString(line + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	k.loaded = false
	k.read()
	return err
}

// Remove takes out the key whose fingerprint is given, keeping the file's
// other lines as they are, and reports whether it was there.
func (k *Keys) Remove(fingerprint string) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	data, err := os.ReadFile(k.path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	var out bytes.Buffer
	found := false
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line)); err == nil && ssh.FingerprintSHA256(key) == fingerprint {
			found = true
			continue
		}
		out.WriteString(line + "\n")
	}
	if !found {
		return false, nil
	}
	err = os.WriteFile(k.path, out.Bytes(), 0o600)
	k.loaded = false
	k.read()
	return true, err
}

// ParseKey reads one key as an authorized_keys line has it, with its
// comment.
func ParseKey(line string) (ssh.PublicKey, string, error) {
	key, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return nil, "", fmt.Errorf("not a public key: %w", err)
	}
	return key, comment, nil
}

// PeerKey is the key a peer ID names, as SSH writes it.
func PeerKey(id peer.ID) (ssh.PublicKey, error) {
	pub, err := id.ExtractPublicKey()
	if err != nil {
		return nil, err
	}
	return sshKey(pub)
}

// sshKey is a libp2p public key as SSH writes it; only Ed25519 keys, which
// mako's hosts have, are.
func sshKey(pub crypto.PubKey) (ssh.PublicKey, error) {
	if pub.Type() != crypto.Ed25519 {
		return nil, fmt.Errorf("p2p: a %s key, not Ed25519", pub.Type())
	}
	raw, err := pub.Raw()
	if err != nil {
		return nil, err
	}
	return ssh.NewPublicKey(ed25519.PublicKey(raw))
}

// Cert is an owner's word that a peer ID is theirs until Expires: the
// owner's SSH public key, and its signature over the two.
type Cert struct {
	Owner   []byte `json:"owner"` // SSH wire format
	Sig     []byte `json:"sig"`   // ssh.Signature, SSH wire format
	Expires int64  `json:"expires"`
}

// certLife is how long a certificate holds, and certRenew how long before
// its end a host signs a new one.
const (
	certLife  = 30 * 24 * time.Hour
	certRenew = 7 * 24 * time.Hour
)

func certData(id peer.ID, expires int64) []byte {
	return []byte("mako-node-cert/1\x00" + id.String() + "\x00" + strconv.FormatInt(expires, 10))
}

// SignCert vouches for id with signer, for certLife.
func SignCert(signer ssh.Signer, id peer.ID) (*Cert, error) {
	expires := time.Now().Add(certLife).Unix()
	sig, err := signer.Sign(rand.Reader, certData(id, expires))
	if err != nil {
		return nil, err
	}
	return &Cert{Owner: signer.PublicKey().Marshal(), Sig: ssh.Marshal(sig), Expires: expires}, nil
}

// Verify returns the owner who vouches for id, if the certificate is
// theirs, for id, and still holds.
func (c *Cert) Verify(id peer.ID) (ssh.PublicKey, error) {
	if c == nil {
		return nil, errors.New("p2p: no certificate")
	}
	if time.Now().Unix() > c.Expires {
		return nil, errors.New("p2p: the certificate has expired")
	}
	owner, err := ssh.ParsePublicKey(c.Owner)
	if err != nil {
		return nil, err
	}
	var sig ssh.Signature
	if err := ssh.Unmarshal(c.Sig, &sig); err != nil {
		return nil, err
	}
	if err := owner.Verify(certData(id, c.Expires), &sig); err != nil {
		return nil, err
	}
	return owner, nil
}

// LoadSigner finds the private half of the SSH public key at pubPath: in
// the SSH agent, or else in the file beside it without the .pub, which
// must not have a passphrase.
func LoadSigner(pubPath string) (ssh.Signer, error) {
	data, err := os.ReadFile(pubPath)
	if err != nil {
		return nil, err
	}
	pub, _, err := ParseKey(string(data))
	if err != nil {
		return nil, err
	}
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if conn, err := net.DialTimeout("unix", sock, 2*time.Second); err == nil {
			signers, _ := agent.NewClient(conn).Signers()
			for _, s := range signers {
				if sameKey(s.PublicKey(), pub) {
					// The agent's connection stays open for the signer.
					return s, nil
				}
			}
			conn.Close()
		}
	}
	priv, err := os.ReadFile(strings.TrimSuffix(pubPath, ".pub"))
	if err != nil {
		return nil, fmt.Errorf("%s is not in the SSH agent, and its private key cannot be read: %w", pubPath, err)
	}
	signer, err := ssh.ParsePrivateKey(priv)
	if err != nil {
		var missing *ssh.PassphraseMissingError
		if errors.As(err, &missing) {
			return nil, fmt.Errorf("%s has a passphrase; add it to the SSH agent", strings.TrimSuffix(pubPath, ".pub"))
		}
		return nil, err
	}
	if !sameKey(signer.PublicKey(), pub) {
		return nil, fmt.Errorf("%s is not the private half of %s", strings.TrimSuffix(pubPath, ".pub"), pubPath)
	}
	return signer, nil
}
