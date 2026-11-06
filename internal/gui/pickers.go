package gui

import "github.com/ironpark/ggui"

// pickerState is the command palette's and quick open's, the two pickers
// over the window; see palette.go and search.go. Its fields are the UI
// goroutine's.
type pickerState struct {
	// The command palette. Each opening gives palette the commands of the
	// moment, and the tabs and sessions it lists, read as it opens.
	PaletteOpen  *ggui.StateValue[bool]
	PaletteQuery *ggui.StateValue[string]
	palette      *picker
	commands     []command
	tabs         []pickRow
	// Quick open, and the files it last read.
	QuickOpen  *ggui.StateValue[bool]
	QuickQuery *ggui.StateValue[string]
	quick      *picker
	quickFiles quickFiles
}

func newPickerState() *pickerState {
	return &pickerState{
		PaletteOpen:  ggui.State(false),
		PaletteQuery: ggui.State(""),
		QuickOpen:    ggui.State(false),
		QuickQuery:   ggui.State(""),
	}
}
