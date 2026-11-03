package gui

import "golang.org/x/sys/windows"

// platformLanguages are the user's display languages, the most preferred
// first, as Windows keeps them: it sets no locale variables.
func platformLanguages() []string {
	langs, _ := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME)
	return langs
}
