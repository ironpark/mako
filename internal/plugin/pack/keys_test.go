package pack

import (
	"encoding/json"
	"strings"
	"testing"
)

// AliasKey writes key with its last base64url character's two unused low
// bits changed: another string the lenient decoder reads as the same key.
func aliasKey(t *testing.T, key string) string {
	t.Helper()
	const alpha = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	last := strings.IndexByte(alpha, key[len(key)-1])
	alt := key[:len(key)-1] + string(alpha[last^0x1])
	a, errA := decodeKey(key)
	b, errB := decodeKey(alt)
	if errA != nil || errB != nil || !a.Equal(b) || alt == key {
		t.Fatalf("no alias of %s: %v %v", key, errA, errB)
	}
	return alt
}

// A key has one spelling: another, which base64url would read as the same
// bytes, is refused wherever a key is read, and compared by its bytes
// wherever one kept is compared.
func TestKeysHaveOneSpelling(t *testing.T) {
	priv := newKey(t)
	canon := PublicKey(priv)
	alt := aliasKey(t, canon)
	if _, err := ParseKey(alt); err == nil {
		t.Fatalf("%s, another spelling of %s, read as a key", alt, canon)
	}
	if _, err := ParseKey(canon); err != nil {
		t.Fatal(err)
	}
	if !SameKey(canon, alt) || SameKey(canon, PublicKey(newKey(t))) || SameKey("", "") || SameKey("rsa:x", "rsa:x") {
		t.Fatal("SameKey does not compare keys by their bytes")
	}
	if got, err := CanonicalKey(alt); err != nil || got != canon {
		t.Fatalf("CanonicalKey(%s) = %s, %v", alt, got, err)
	}
	if Fingerprint(alt) != Fingerprint(canon) || Fingerprint(canon) == "" {
		t.Fatal("a key's spellings have different fingerprints")
	}

	// A package whose signature names the key in another spelling.
	data, pkg, err := Build(testFiles(), priv)
	if err != nil {
		t.Fatal(err)
	}
	var sig Signature
	if err := json.Unmarshal(pkg.sig, &sig); err != nil {
		t.Fatal(err)
	}
	sig.Key = alt
	sigData, _ := json.Marshal(sig)
	var entries []entry
	for n, d := range pkg.Files {
		entries = append(entries, entry{name: n, data: d})
	}
	entries = append(entries, entry{name: SignatureName, data: sigData})
	if _, err := Open(rawZip(t, entries...)); err == nil || !strings.Contains(err.Error(), "another spelling") {
		t.Fatalf("a signature naming another spelling of its key opened: %v", err)
	}
	if _, err := Open(data); err != nil {
		t.Fatal(err)
	}
}
