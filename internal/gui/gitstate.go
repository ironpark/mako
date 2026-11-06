package gui

import (
	"maps"

	"github.com/ironpark/ggui"
)

// gitState is the Git panel's: the Changes and History tabs, the commit
// being written, and the names git operations ask for. Its fields are the
// UI goroutine's.
type gitState struct {
	// The project's changes, the folders closed in their tree, and the
	// row chosen.
	View   *ggui.StateValue[gitView]
	Closed *ggui.StateValue[map[string]bool]
	Sel    *ggui.StateValue[string]
	// The files left out of the next commit, its message, and whether it
	// amends the last; the operation under way and what the last said.
	Skip    *ggui.StateValue[map[string]bool]
	Message *ggui.StateValue[string]
	Amend   *ggui.StateValue[bool]
	Busy    *ggui.StateValue[string]
	Base    *ggui.StateValue[string] // the branch the changes are read since, "" for HEAD
	Note    *ggui.StateValue[gitNote]
	// A name a git operation asks for; see gitactions.go.
	Ask     *ggui.StateValue[gitAsk]
	AskOpen *ggui.StateValue[bool]
	AskText *ggui.StateValue[string]
	AskErr  *ggui.StateValue[string]
	// The panel's sections, open or folded, and how its height is split.
	ChangesOpen *ggui.StateValue[bool]
	HistoryOpen *ggui.StateValue[bool]
	StashOpen   *ggui.StateValue[bool]
	BranchOpen  *ggui.StateValue[bool]
	Split       *ggui.StateValue[float64]
	// The History tab: the commits, and the one chosen.
	History *ggui.StateValue[historyView]
	Commit  *ggui.StateValue[commitView]
	// historyReading is set while the history is asked for, and reading
	// while the changes are.
	historyReading bool
	reading        bool
}

func newGitState() *gitState {
	return &gitState{
		View:    ggui.State(gitView{}).WithEqual(deepEqual),
		Closed:  ggui.State(map[string]bool{}).WithEqual(maps.Equal),
		Sel:     ggui.State(""),
		Skip:    ggui.State(map[string]bool{}).WithEqual(maps.Equal),
		Message: ggui.State(""),
		Amend:   ggui.State(false),
		Busy:    ggui.State(""),
		Base:    ggui.State(""),
		Note:    ggui.State(gitNote{}),
		// Their functions do not compare: each asking is new.
		Ask:         ggui.State(gitAsk{}).WithEqual(func(a, b gitAsk) bool { return false }),
		AskOpen:     ggui.State(false),
		AskText:     ggui.State(""),
		AskErr:      ggui.State(""),
		ChangesOpen: ggui.State(true),
		HistoryOpen: ggui.State(true),
		StashOpen:   ggui.State(false),
		BranchOpen:  ggui.State(false),
		Split:       ggui.State(0.55),
		History:     ggui.State(historyView{}).WithEqual(deepEqual),
		Commit:      ggui.State(commitView{}).WithEqual(deepEqual),
	}
}
