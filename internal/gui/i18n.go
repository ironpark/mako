package gui

import (
	"fmt"
	"os"
	"os/exec"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
)

// The window is written in English and translated where it is drawn: T
// looks a string up in the catalog of the language the settings choose,
// and falls back to the English. The English is the key, so a string reads
// in the code as it does on screen. A language change rebuilds the window,
// so every label is drawn again in it.

// languages are the window's languages, how each names itself, and its
// translations; English, the strings' own, needs none.
var languages = []struct {
	Code, Name string
	Catalog    map[string]string
}{
	{"en", "English", nil},
	{"ko", "한국어", catalogKo},
}

// catalog is a language's translations, and whether the window has it.
func catalog(code string) (map[string]string, bool) {
	for _, l := range languages {
		if l.Code == code {
			return l.Catalog, true
		}
	}
	return nil, false
}

// current is the language the window is drawn in now.
var current atomic.Pointer[string]

func currentLanguage() string {
	if l := current.Load(); l != nil {
		return *l
	}
	return "en"
}

func setLanguage(code string) { current.Store(&code) }

// T is s in the window's language.
func T(s string) string {
	c, _ := catalog(currentLanguage())
	if t, ok := c[s]; ok {
		return t
	}
	return s
}

// Tf is fmt.Sprintf with the format translated.
func Tf(format string, args ...any) string { return fmt.Sprintf(T(format), args...) }

// resolveLanguage is the language a setting draws the window in: the one
// it names, or for "system" the platform's when the window has it.
func resolveLanguage(setting string) string {
	if _, ok := catalog(setting); ok {
		return setting
	}
	return systemLanguage()
}

// systemLanguage is the first of the user's languages the window has, else
// English: macOS keeps the order in AppleLanguages, elsewhere the locale
// variables say it.
var systemLanguage = sync.OnceValue(func() string {
	var prefs []string
	for _, v := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if s := os.Getenv(v); s != "" && s != "C" && s != "POSIX" {
			prefs = append(prefs, s)
			break
		}
	}
	if goruntime.GOOS == "darwin" {
		if out, err := exec.Command("defaults", "read", "-g", "AppleLanguages").Output(); err == nil {
			// ( "ko-KR", "en-KR" ), the first the most preferred.
			langs := strings.FieldsFunc(string(out), func(r rune) bool { return strings.ContainsRune("(), \"\n\t", r) })
			prefs = append(langs, prefs...)
		}
	}
	for _, p := range prefs {
		code := strings.ToLower(p)
		if i := strings.IndexAny(code, "-_."); i >= 0 {
			code = code[:i]
		}
		if _, ok := catalog(code); ok {
			return code
		}
	}
	return "en"
})
