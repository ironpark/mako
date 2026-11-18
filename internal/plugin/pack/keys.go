package pack

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
)

// A publisher key is an ed25519 key. Its public half is written
// "ed25519:" and its 32 bytes in unpadded base64url, in indexes,
// signatures and pins; its private half stays on the publisher's machine,
// in a PEM file of PKCS #8 that keygen makes and nothing else in mako
// reads. A key's fingerprint, which a person compares, is the first 16
// bytes of the public key's SHA-256, in hex, in groups of four.

const keyPrefix = "ed25519:"

// EncodeKey writes a public key.
func EncodeKey(pub ed25519.PublicKey) string {
	return keyPrefix + base64.RawURLEncoding.EncodeToString(pub)
}

// ParseKey reads a public key written by EncodeKey, in that spelling
// alone. Base64url leaves the last character's low bits unused, so one
// key could be written several ways; a key is compared as written in many
// places (a blocklist's entry, a pin, an index against a signature), so
// a spelling other than EncodeKey's is refused rather than read.
func ParseKey(s string) (ed25519.PublicKey, error) {
	pub, err := decodeKey(s)
	if err != nil {
		return nil, err
	}
	if EncodeKey(pub) != s {
		return nil, errors.New("a publisher key is written as mako writes it: this one is another spelling of a key, which is refused")
	}
	return pub, nil
}

// decodeKey reads a key's 32 bytes in any spelling base64url decodes.
func decodeKey(s string) (ed25519.PublicKey, error) {
	rest, ok := strings.CutPrefix(s, keyPrefix)
	if !ok {
		return nil, fmt.Errorf("a publisher key starts %q", keyPrefix)
	}
	b, err := base64.RawURLEncoding.DecodeString(rest)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("a publisher key is 32 bytes in unpadded base64url")
	}
	return ed25519.PublicKey(b), nil
}

// SameKey says a and b are the same publisher key, by their bytes, so
// that a key kept before ParseKey refused other spellings (a pin of an
// older mako) is still the key it names. A key that does not read is no
// key: it is the same as none, not even itself.
func SameKey(a, b string) bool {
	ka, errA := decodeKey(a)
	kb, errB := decodeKey(b)
	return errA == nil && errB == nil && ka.Equal(kb)
}

// CanonicalKey is a key in EncodeKey's spelling, read in any spelling;
// for keys kept before other spellings were refused.
func CanonicalKey(s string) (string, error) {
	pub, err := decodeKey(s)
	if err != nil {
		return "", err
	}
	return EncodeKey(pub), nil
}

// Fingerprint is a key's fingerprint, or "" for one that does not read.
// Every spelling of a key has the key's fingerprint.
func Fingerprint(key string) string {
	pub, err := decodeKey(key)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(pub)
	h := hex.EncodeToString(sum[:16])
	var parts []string
	for i := 0; i < len(h); i += 4 {
		parts = append(parts, h[i:i+4])
	}
	return strings.Join(parts, "-")
}

// Keygen makes a publisher key, writing its private half to path, which
// must not exist, readable by its owner alone; it returns the public key.
func Keygen(path string) (string, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("%s exists: a key is never written over", path)
		}
		return "", err
	}
	if err := pem.Encode(f, &pem.Block{Type: "PRIVATE KEY", Bytes: der}); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return EncodeKey(pub), nil
}

// LoadKey reads a private key Keygen wrote.
func LoadKey(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("%s is not a PEM private key", path)
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	priv, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s is not an ed25519 key", path)
	}
	return priv, nil
}

// PublicKey is a private key's public half, written.
func PublicKey(priv ed25519.PrivateKey) string {
	return EncodeKey(priv.Public().(ed25519.PublicKey))
}
