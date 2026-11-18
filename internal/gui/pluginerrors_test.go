package gui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/gui/i18n"
	"github.com/ironpark/mako/internal/protocol"
)

// A coded error of the store's is said in the window's language, naming
// what the daemon named; one the window does not know, or of an older
// daemon, is said as the daemon wrote it.
func TestStoreErrorsAreSaidInTheWindowsLanguage(t *testing.T) {
	was := i18n.Current()
	t.Cleanup(func() { i18n.Set(was) })
	i18n.Set("ko")
	hash := &protocol.CodedError{Code: protocol.CodeHashMismatch, Message: "remote: the package of ci 1.0.0 downloaded is not the one…",
		Args: []string{"ci", "1.0.0", "https://github.com/o/r"}}
	got := errorText(fmt.Errorf("installing: %w", hash))
	if !strings.Contains(got, "ci 1.0.0 패키지가 https://github.com/o/r에 등록된 것과 다릅니다") {
		t.Fatalf("the hash mismatch in Korean: %q", got)
	}
	api := &protocol.CodedError{Code: protocol.CodeAPIMismatch, Message: "x", Args: []string{"ci", "https://r"}}
	if got := errorText(api); !strings.HasPrefix(got, "https://r에는 이 mako가 실행할 수 있는 ci 버전이 없습니다") {
		t.Fatalf("the reordered sentence: %q", got)
	}
	for _, code := range []string{protocol.CodeRegistryUnreachable, protocol.CodePinnedKey, protocol.CodePermissionsDiffer,
		protocol.CodePluginBlocked, protocol.CodeDowngrade, protocol.CodeDevFolder, protocol.CodeTimedOut, protocol.CodeTooLarge} {
		tmpl := codedErrors[code]
		args := make([]string, verbs(tmpl))
		for i := range args {
			args[i] = fmt.Sprint("arg", i)
		}
		got := errorText(&protocol.CodedError{Code: code, Message: "English", Args: args})
		if got == "English" || strings.Contains(got, "%!") || !strings.Contains(got, "arg0") || got == tmpl {
			t.Errorf("%s: %q", code, got)
		}
	}
	// Unknown codes, args that do not fit, and plain errors stay as written.
	for _, err := range []error{
		&protocol.CodedError{Code: "something_new", Message: "remote: something new"},
		&protocol.CodedError{Code: protocol.CodeHashMismatch, Message: "remote: too few", Args: []string{"ci"}},
		errors.New("remote: plain"),
	} {
		if got := errorText(err); got != err.Error() {
			t.Errorf("%v: %q", err, got)
		}
	}
	i18n.Set("en")
	if got := errorText(hash); !strings.HasPrefix(got, "The package of ci 1.0.0 is not the one https://github.com/o/r lists") {
		t.Fatalf("in English: %q", got)
	}
}
