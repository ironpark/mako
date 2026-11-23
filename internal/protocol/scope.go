package protocol

import (
	"encoding/base64"
	"strings"
	"unicode/utf8"
)

// ScopeFolder is the plugin scope kind for a single folder on one node
// (revision 46). Its canonical key is "folder:<node>/<base64url-nopad(path)>",
// where <node> is "local" when no explicit node ID is given.
const ScopeFolder = "folder"

// LocalScopeNode is the node token used in canonical folder scope keys when
// the folder sits on the local node (empty node ID).
const LocalScopeNode = "local"

// FolderScope builds the canonical scope key for a folder on a node:
// "folder:<node>/<base64url-nopad(path)>". It returns "" if path is empty,
// contains NUL bytes, is not valid UTF-8, or node contains invalid characters.
// An empty node is encoded as LocalScopeNode ("local").
func FolderScope(node, path string) string {
	if path == "" || strings.ContainsRune(path, 0) || !utf8.ValidString(path) {
		return ""
	}
	if node == "" {
		node = LocalScopeNode
	}
	if !validFolderScopeNode(node) {
		return ""
	}
	return ScopeFolder + ":" + node + "/" + base64.RawURLEncoding.EncodeToString([]byte(path))
}

// ParseFolderScope parses a canonical folder scope key created by FolderScope.
// On success it returns the node token (e.g. "local" or a peer node ID), the
// decoded folder path, and true.
func ParseFolderScope(key string) (node, path string, ok bool) {
	rest, hasPrefix := strings.CutPrefix(key, ScopeFolder+":")
	if !hasPrefix {
		return "", "", false
	}
	node, enc, hasSlash := strings.Cut(rest, "/")
	if !hasSlash || enc == "" || !validFolderScopeNode(node) {
		return "", "", false
	}
	if strings.ContainsAny(enc, "/\r\n") {
		return "", "", false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(enc)
	if err != nil || len(raw) == 0 {
		return "", "", false
	}
	// Ensure canonical encoding (rejects non-base64url characters or padding).
	if base64.RawURLEncoding.EncodeToString(raw) != enc {
		return "", "", false
	}
	decoded := string(raw)
	if strings.ContainsRune(decoded, 0) || !utf8.ValidString(decoded) {
		return "", "", false
	}
	return node, decoded, true
}

// validFolderScopeNode checks that node is a safe ASCII identifier segment
// without "/" or ".." (compatible with plugin scopeIDRe).
func validFolderScopeNode(node string) bool {
	if len(node) == 0 || len(node) > 128 || strings.Contains(node, "..") {
		return false
	}
	for i := 0; i < len(node); i++ {
		c := node[i]
		isAlphaNum := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
		if i == 0 {
			if !isAlphaNum && c != '_' {
				return false
			}
			continue
		}
		if !isAlphaNum && c != '.' && c != '_' && c != '+' && c != '-' {
			return false
		}
	}
	return true
}
