package gui

import "github.com/ironpark/mako/internal/gui/i18n"

// T is s in the window's language; see package i18n. The window calls it
// by this name, which the catalogs' check finds its strings by.
func T(s string) string { return i18n.T(s) }

// Tf is fmt.Sprintf with the format translated.
func Tf(format string, args ...any) string { return i18n.Tf(format, args...) }
