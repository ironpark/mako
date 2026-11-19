package gui

import (
	"errors"
	"strings"

	"github.com/ironpark/mako/internal/gui/i18n"
	"github.com/ironpark/mako/internal/protocol"
)

// The store's errors, and some of plugins' actions, come with a code and
// what their sentence names (protocol.CodedError, revision 34): the
// window says them in its own language, rather than the daemon's English
// inside a window in Korean. A code the window does not know, or an
// error of an older daemon, is shown as the daemon wrote it.

// codedErrors are the sentences of the coded errors, by code, with what
// each names, in order, as protocol's Code constants say.
var codedErrors = map[string]string{
	protocol.CodeRegistryUnreachable: i18n.N("The registry %s could not be read: it is out of reach, or its index is not one mako reads."),
	protocol.CodeRegistryNeeded:      i18n.N("Several registries list %s, perhaps by different publishers: choose the one to install from."),
	protocol.CodeNotListed:           i18n.N("No registry of this node lists %s."),
	protocol.CodeAPIMismatch:         i18n.N("%[2]s lists no version of %[1]s that this mako can run: it needs a newer or older mako."),
	protocol.CodePluginBlocked:       i18n.N("%s %s is blocked: %s"),
	protocol.CodePinnedKey:           i18n.N("%s is signed by another publisher key than the one this node pinned (%s): an update must be signed by the same key. Remove the plugin to install the other publisher's."),
	protocol.CodeInstalledAlready:    i18n.N("%s %s is installed already."),
	protocol.CodeDowngrade:           i18n.N("The registry lists %s %s, older than the %s installed: mako does not go back a version. Remove the plugin and install it again to."),
	protocol.CodeDevFolder:           i18n.N("plugins/%s is a folder of a plugin in development: mako neither replaces nor removes it. Move it away first."),
	protocol.CodePackageUnreachable:  i18n.N("The package of %s %s did not download."),
	protocol.CodeTimedOut:            i18n.N("The package of %s %s took too long to download."),
	protocol.CodeTooLarge:            i18n.N("The package of %s %s is larger than a plugin may be."),
	protocol.CodeHashMismatch:        i18n.N("The package of %s %s is not the one %s lists: it was changed on the server or on the way. Nothing was installed."),
	protocol.CodeBadPackage:          i18n.N("The package of %s %s is not a valid plugin package. Nothing was installed."),
	protocol.CodeSignatureMismatch:   i18n.N("The package of %s %s is not signed by the publisher key %s lists. Nothing was installed."),
	protocol.CodePermissionsDiffer:   i18n.N("The package of %s %s asks for other permissions than %s lists for it. Nothing was installed."),
	protocol.CodeNotInstalled:        i18n.N("%s is not installed."),
}

// errorText is err as the window says it: a coded error in the window's
// language, any other as the daemon wrote it.
func errorText(err error) string {
	if ce, ok := errors.AsType[*protocol.CodedError](err); ok {
		if tmpl, ok := codedErrors[ce.Code]; ok && verbs(tmpl) == len(ce.Args) {
			args := make([]any, len(ce.Args))
			for i, a := range ce.Args {
				args[i] = a
			}
			return Tf(tmpl, args...)
		}
	}
	return err.Error()
}

// verbs counts the strings a sentence names: %s, or %[n]s by the highest n.
func verbs(tmpl string) int {
	n := strings.Count(tmpl, "%s")
	for i := 1; i <= 9; i++ {
		if strings.Contains(tmpl, "%["+string(rune('0'+i))+"]s") {
			n = max(n, i)
		}
	}
	return n
}
