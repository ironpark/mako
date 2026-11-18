package keychain

import (
	"errors"
	"testing"
)

// A test binary never reaches the user's keychain, whatever the system
// has: code under test is handed a Fake.
func TestSystemIsNoneInATest(t *testing.T) {
	kc, err := System()
	if kc != nil || err == nil {
		t.Fatalf("System() = %v, %v in a test", kc, err)
	}
}

func TestFake(t *testing.T) {
	var f Fake
	if _, err := f.Get("s", "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty Get: %v", err)
	}
	if err := f.Set("s", "a", "v"); err != nil {
		t.Fatal(err)
	}
	if v, err := f.Get("s", "a"); err != nil || v != "v" {
		t.Fatalf("Get = %q, %v", v, err)
	}
	if _, err := f.Get("s", "b"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another account: %v", err)
	}
	if err := f.Delete("s", "a"); err != nil {
		t.Fatal(err)
	}
	if err := f.Delete("s", "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Delete: %v", err)
	}
	f.Fail = errors.New("locked")
	if err := f.Set("s", "a", "v"); err == nil {
		t.Fatal("a failing keychain took a write")
	}
}
