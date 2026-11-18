package keychain

import (
	"errors"
	"fmt"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// The macOS Keychain is reached through the Security framework's generic
// password items (SecItem*), with Core Foundation's types, both called
// with purego, so the package builds without cgo. An item belongs to the
// login keychain, and its access list trusts the program that made it:
// a mako built anew is asked about once by the system, as any program
// whose signature changed is.

const (
	errSecSuccess           = 0
	errSecItemNotFound      = -25300
	errSecDuplicateItem     = -25299
	kCFStringEncodingUTF8   = 0x08000100
	frameworkSecurity       = "/System/Library/Frameworks/Security.framework/Security"
	frameworkCoreFoundation = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
)

type security struct {
	cfStringCreate     func(alloc uintptr, bytes *byte, n int, enc uint32, external bool) uintptr
	cfDataCreate       func(alloc uintptr, bytes *byte, n int) uintptr
	cfDataLength       func(data uintptr) int
	cfDataBytes        func(data uintptr) *byte
	cfDictionaryCreate func(alloc uintptr, keys, values *uintptr, n int, keyCB, valueCB uintptr) uintptr
	cfRelease          func(ref uintptr)

	itemAdd      func(attrs uintptr, result uintptr) int32
	itemCopy     func(query uintptr, result *uintptr) int32
	itemUpdate   func(query, attrs uintptr) int32
	itemDelete   func(query uintptr) int32
	errorMessage func(status int32, reserved uintptr) uintptr
	cfStringUTF8 func(str uintptr, buf *byte, n int, enc uint32) bool

	keyCB, valueCB                     uintptr // the dictionaries' callbacks, by address
	cfTrue                             uintptr
	class, genericPassword             uintptr
	service, account, label, valueData uintptr
	returnData, matchLimit, limitOne   uintptr
}

// global reads a framework's constant: the value at a symbol's address.
func global(lib uintptr, name string) (uintptr, error) {
	addr, err := purego.Dlsym(lib, name)
	if err != nil {
		return 0, err
	}
	// Through the variable's address, as the address is C memory held as
	// an integer.
	return **(**uintptr)(unsafe.Pointer(&addr)), nil
}

var loadSecurity = sync.OnceValues(func() (*security, error) {
	sec, err := purego.Dlopen(frameworkSecurity, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return nil, fmt.Errorf("the Security framework: %w", err)
	}
	cf, err := purego.Dlopen(frameworkCoreFoundation, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return nil, fmt.Errorf("Core Foundation: %w", err)
	}
	s := &security{}
	purego.RegisterLibFunc(&s.cfStringCreate, cf, "CFStringCreateWithBytes")
	purego.RegisterLibFunc(&s.cfDataCreate, cf, "CFDataCreate")
	purego.RegisterLibFunc(&s.cfDataLength, cf, "CFDataGetLength")
	purego.RegisterLibFunc(&s.cfDataBytes, cf, "CFDataGetBytePtr")
	purego.RegisterLibFunc(&s.cfDictionaryCreate, cf, "CFDictionaryCreate")
	purego.RegisterLibFunc(&s.cfRelease, cf, "CFRelease")
	purego.RegisterLibFunc(&s.cfStringUTF8, cf, "CFStringGetCString")
	purego.RegisterLibFunc(&s.itemAdd, sec, "SecItemAdd")
	purego.RegisterLibFunc(&s.itemCopy, sec, "SecItemCopyMatching")
	purego.RegisterLibFunc(&s.itemUpdate, sec, "SecItemUpdate")
	purego.RegisterLibFunc(&s.itemDelete, sec, "SecItemDelete")
	purego.RegisterLibFunc(&s.errorMessage, sec, "SecCopyErrorMessageString")
	if s.keyCB, err = purego.Dlsym(cf, "kCFTypeDictionaryKeyCallBacks"); err != nil {
		return nil, err
	}
	if s.valueCB, err = purego.Dlsym(cf, "kCFTypeDictionaryValueCallBacks"); err != nil {
		return nil, err
	}
	for _, g := range []struct {
		lib  uintptr
		name string
		to   *uintptr
	}{
		{cf, "kCFBooleanTrue", &s.cfTrue},
		{sec, "kSecClass", &s.class},
		{sec, "kSecClassGenericPassword", &s.genericPassword},
		{sec, "kSecAttrService", &s.service},
		{sec, "kSecAttrAccount", &s.account},
		{sec, "kSecAttrLabel", &s.label},
		{sec, "kSecValueData", &s.valueData},
		{sec, "kSecReturnData", &s.returnData},
		{sec, "kSecMatchLimit", &s.matchLimit},
		{sec, "kSecMatchLimitOne", &s.limitOne},
	} {
		v, err := global(g.lib, g.name)
		if err != nil || v == 0 {
			return nil, fmt.Errorf("the Security framework has no %s", g.name)
		}
		*g.to = v
	}
	return s, nil
})

// cfString makes a CFString of s, which the caller releases.
func (s *security) cfString(v string) uintptr {
	b := []byte(v)
	if len(b) == 0 {
		b = []byte{0}
		return s.cfStringCreate(0, &b[0], 0, kCFStringEncodingUTF8, false)
	}
	return s.cfStringCreate(0, &b[0], len(b), kCFStringEncodingUTF8, false)
}

func (s *security) cfData(v string) uintptr {
	b := []byte(v)
	if len(b) == 0 {
		b = []byte{0}
		return s.cfDataCreate(0, &b[0], 0)
	}
	return s.cfDataCreate(0, &b[0], len(b))
}

// dict makes a CFDictionary of pairs, key then value, which the caller
// releases. The dictionary retains what it holds.
func (s *security) dict(pairs ...uintptr) uintptr {
	n := len(pairs) / 2
	keys, values := make([]uintptr, n), make([]uintptr, n)
	for i := range n {
		keys[i], values[i] = pairs[2*i], pairs[2*i+1]
	}
	return s.cfDictionaryCreate(0, &keys[0], &values[0], n, s.keyCB, s.valueCB)
}

// statusError is a Security status as an error, in the system's words.
func (s *security) statusError(op string, status int32) error {
	msg := ""
	if ref := s.errorMessage(status, 0); ref != 0 {
		buf := make([]byte, 512)
		if s.cfStringUTF8(ref, &buf[0], len(buf), kCFStringEncodingUTF8) {
			for i, c := range buf {
				if c == 0 {
					msg = string(buf[:i])
					break
				}
			}
		}
		s.cfRelease(ref)
	}
	if msg == "" {
		return fmt.Errorf("keychain %s: OSStatus %d", op, status)
	}
	return fmt.Errorf("keychain %s: %s (%d)", op, msg, status)
}

type macKeychain struct{ s *security }

func system() (Keychain, error) {
	s, err := loadSecurity()
	if err != nil {
		return nil, err
	}
	k := macKeychain{s}
	// A search for an item no one makes says whether there is a keychain
	// to search, without writing anything.
	if _, err := k.Get("mako.keychain.probe", "probe"); err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	return k, nil
}

func (macKeychain) Name() string { return "macOS Keychain" }

// query is the item of service and account, with more pairs; it and the
// strings it made are released by the call it returns.
func (k macKeychain) query(service, account string, more ...uintptr) (uintptr, func()) {
	s := k.s
	svc, acc := s.cfString(service), s.cfString(account)
	q := s.dict(append([]uintptr{s.class, s.genericPassword, s.service, svc, s.account, acc}, more...)...)
	return q, func() {
		s.cfRelease(q)
		s.cfRelease(svc)
		s.cfRelease(acc)
	}
}

func (k macKeychain) Get(service, account string) (string, error) {
	s := k.s
	q, free := k.query(service, account, s.returnData, s.cfTrue, s.matchLimit, s.limitOne)
	defer free()
	var out uintptr
	switch st := s.itemCopy(q, &out); st {
	case errSecSuccess:
	case errSecItemNotFound:
		return "", ErrNotFound
	default:
		return "", s.statusError("read", st)
	}
	defer s.cfRelease(out)
	n := s.cfDataLength(out)
	if n == 0 {
		return "", nil
	}
	return string(unsafe.Slice(s.cfDataBytes(out), n)), nil
}

func (k macKeychain) Set(service, account, value string) error {
	s := k.s
	data := s.cfData(value)
	defer s.cfRelease(data)
	q, free := k.query(service, account)
	defer free()
	attrs := s.dict(s.valueData, data)
	defer s.cfRelease(attrs)
	switch st := s.itemUpdate(q, attrs); st {
	case errSecSuccess:
		return nil
	case errSecItemNotFound:
	default:
		return s.statusError("write", st)
	}
	label := s.cfString(service + ": " + account)
	defer s.cfRelease(label)
	add, freeAdd := k.query(service, account, s.valueData, data, s.label, label)
	defer freeAdd()
	switch st := s.itemAdd(add, 0); st {
	case errSecSuccess:
		return nil
	case errSecDuplicateItem: // made meanwhile: write over it
		if st := s.itemUpdate(q, attrs); st != errSecSuccess {
			return s.statusError("write", st)
		}
		return nil
	default:
		return s.statusError("write", st)
	}
}

func (k macKeychain) Delete(service, account string) error {
	s := k.s
	q, free := k.query(service, account)
	defer free()
	switch st := s.itemDelete(q); st {
	case errSecSuccess:
		return nil
	case errSecItemNotFound:
		return ErrNotFound
	default:
		return s.statusError("delete", st)
	}
}
