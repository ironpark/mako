// Package pack is the .makoplugin package: a plugin's folder as a zip
// archive, with the publisher's ed25519 signature of its contents. It is
// what a registry lists and a node installs (docs/plugins.md,
// "Publishing"). The format is defined here so that another program can
// make and check one:
//
//   - A zip archive of regular files only: no folders, symbolic links or
//     other kinds, nothing encrypted, stored or deflated. The files are
//     the plugin's folder's, by their paths there, and one more,
//     SignatureName, at the root.
//   - A path is 1 to 8 parts joined by "/", each of letters, digits and
//     "._-", not starting with "." or "-", 255 bytes at most in all. No
//     two paths are the same when upper and lower case are taken alike.
//   - At most MaxFiles files, each MaxFileBytes at most and MaxTotalBytes
//     in all once read, and the archive MaxPackageBytes.
//   - The contents' digest is the SHA-256 of "mako-plugin-contents/1\n"
//     followed, for each file but the signature, in the byte order of
//     their paths, by the path, "\n", the file's size in decimal, "\n",
//     and the file's bytes.
//   - The signature file is JSON (Signature): the format, the manifest's
//     id and version, the publisher key, the digest as "sha256:<hex>",
//     and an ed25519 signature, in unpadded base64url, of the statement
//     "mako-plugin-signature/1\nid <id>\nversion <version>\ncontents
//     sha256:<hex>\n".
//
// Packing writes the files in the order of their paths, stored, dated
// 1980-01-01, the signature last, so a folder packs to the same bytes
// every time.
package pack

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The bounds of a package.
const (
	MaxPackageBytes = 8 << 20
	MaxFiles        = 256
	MaxFileBytes    = 2 << 20
	MaxTotalBytes   = 8 << 20
	maxPathBytes    = 255
	maxPathParts    = 8
	maxSignature    = 4 << 10
)

// SignatureName is the signature's file, at the package's root.
const SignatureName = "mako-signature.json"

// The formats a package's parts name.
const (
	contentsFormat  = "mako-plugin-contents/1"
	SignatureFormat = "mako-plugin-signature/1"
)

var pathPart = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)

// CheckPath refuses a path a package may not hold: absolute, with "..",
// a backslash, a part starting with a dot, an empty part, or too long or
// deep. It is checked before anything is written by it.
func CheckPath(p string) error {
	if p == "" || len(p) > maxPathBytes {
		return fmt.Errorf("%q: a path is 1 to %d bytes", trim(p), maxPathBytes)
	}
	parts := strings.Split(p, "/")
	if len(parts) > maxPathParts {
		return fmt.Errorf("%q is deeper than %d folders", trim(p), maxPathParts-1)
	}
	for _, part := range parts {
		if !pathPart.MatchString(part) {
			return fmt.Errorf("%q is not a plain relative path (letters, digits and ._- in each part, none starting with a dot)", trim(p))
		}
	}
	return nil
}

// trim shortens what an error quotes.
func trim(s string) string {
	if len(s) > 80 {
		return s[:80] + "…"
	}
	return s
}

// Digest is the contents' digest of files, by path, the signature's
// file left out: "sha256:" and its hex.
func Digest(files map[string][]byte) string {
	paths := make([]string, 0, len(files))
	for p := range files {
		if p != SignatureName {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)
	h := sha256.New()
	h.Write([]byte(contentsFormat + "\n"))
	for _, p := range paths {
		h.Write([]byte(p + "\n" + strconv.Itoa(len(files[p])) + "\n"))
		h.Write(files[p])
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// Statement is what a publisher signs for a version of a plugin whose
// contents have the digest.
func Statement(id, version, digest string) []byte {
	return []byte(SignatureFormat + "\nid " + id + "\nversion " + version + "\ncontents " + digest + "\n")
}

// Signature is the signature's file.
type Signature struct {
	Format    string `json:"format"`
	ID        string `json:"id"`
	Version   string `json:"version"`
	Key       string `json:"key"`
	Contents  string `json:"contents"`
	Signature string `json:"signature"`
}
