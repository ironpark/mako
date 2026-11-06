package gui

import (
	"maps"

	"github.com/ironpark/ggui"
)

// filesState is the Files tab's; see files.go. Its fields are the UI
// goroutine's.
type filesState struct {
	// The folders read, by dirKey; those open; the row chosen; and the
	// file shown, with ImageOpen showing an image of it enlarged.
	Dirs      *ggui.StateValue[map[string]dirListing]
	Open      *ggui.StateValue[map[string]bool]
	Sel       *ggui.StateValue[string]
	Preview   *ggui.StateValue[filePreview]
	ImageOpen *ggui.StateValue[bool]
	// A file operation asked of the user; Note shows a failure under the
	// Files header once its dialog has closed.
	Ops  fileOps
	Note *ggui.StateValue[string]
	// Clip is the file or folder copied or cut for Paste, in the Files tab
	// or a node's file browser; Path is "" for none.
	Clip *ggui.StateValue[pathDrag]
}

func newFilesState() *filesState {
	return &filesState{
		Dirs:      ggui.State(map[string]dirListing{}).WithEqual(deepEqual),
		Open:      ggui.State(map[string]bool{}).WithEqual(maps.Equal),
		Sel:       ggui.State(""),
		Preview:   ggui.State(filePreview{}).WithEqual(filePreview.same),
		ImageOpen: ggui.State(false),
		Ops:       newFileOps(),
		Note:      ggui.State(""),
		Clip:      ggui.State(pathDrag{}),
	}
}
