package agy

import "github.com/ironpark/mako/internal/detect"

// ScreenRules read what agy shows, which its hooks do not tell: the
// dialogs it asks in, a permission, a question or a folder's trust,
// full-screen/modal overlays (/skills, /resume) that hold state, and
// the prompt it comes back to from a turn interrupted, which runs no Stop.
// They follow herdr's rules for Antigravity (github.com/herdrdev/herdr,
// Apache-2.0), checked against agy 1.5.12–1.5.18's screens in testdata.
//
// A dialog takes the prompt box's place and ends in a row of keys over
// the footer, "esc to cancel" as while a turn runs:
//
//	permission  ↑/↓ Navigate · tab Amend · ctrl+g edit/expand command
//	question    ↑/↓ Navigate · enter Select · esc Skip (or space Toggle / Write-in / enter Submit All)
//	trust       ↑/↓ Navigate · enter Confirm
//	slash menu  ↑/↓ Navigate · enter Select · tab Complete (at its prompt)
//	overlay     Keyboard: ↑/↓ Navigate ... esc Cancel / esc Go back
//
// A turn draws a braille spinner over the prompt box, and the footer
// "esc to cancel" right under the box's bottom rule; at its prompt the
// footer reads "? for shortcuts". Bottom regions allow a few extra rows
// so a stacked custom statusLine or below-composer dock never hides them.
func (*Agent) ScreenRules() *detect.Rules { return screenRules }

// overlayView matches agy's modal / alt-screen views (/skills, /resume,
// settings tabs) whose "Keyboard:" control bar takes over the prompt.
var overlayView = detect.Rule{
	ID: "overlay_view", Priority: 350, Region: detect.Bottom(5), Hold: true,
	Contains: []string{"keyboard:"},
	Any: []detect.Gate{
		{Contains: []string{"esc cancel"}},
		{Contains: []string{"esc go back"}},
		{Contains: []string{"esc close"}},
		{Contains: []string{"esc back"}},
		{Contains: []string{"esc exit"}},
		{Contains: []string{"tab switch tab"}},
	},
}

var screenRules = detect.MustCompile([]detect.Rule{
	overlayView,
	{
		ID: "permission_prompt", State: detect.Blocked, Priority: 300, Region: detect.Bottom(5),
		Contains: []string{"navigate", "tab amend"},
	},
	{
		ID: "question_prompt", State: detect.Blocked, Priority: 290, Region: detect.Bottom(5),
		Any: []detect.Gate{
			{Contains: []string{"navigate", "esc skip"}},
			{Contains: []string{"enter select", "esc skip"}},
			{Contains: []string{"space toggle", "esc skip"}},
			{Contains: []string{"write-in", "esc skip"}},
			{Regex: []string{`(?mi)^\s*(?:.*·\s*)?enter\s+submit\s+all(?:\s*·|\s*$)`}},
		},
	},
	{
		ID: "trust_prompt", State: detect.Blocked, Priority: 280, Region: detect.Bottom(10),
		Contains: []string{"do you trust the contents of this project?", "yes, i trust this folder", "enter confirm"},
	},
	{
		// Before 1.1.28 a permission was asked without the row of keys.
		ID: "legacy_permission_prompt", State: detect.Blocked, Priority: 270, Region: detect.Bottom(20),
		Contains: []string{"requesting permission for:"},
		Any:      []detect.Gate{{Contains: []string{"do you want to proceed?"}}, {Contains: []string{"tab amend", "edit command"}}},
	},
	{
		// The slash commands' menu over the footer, at the prompt.
		ID: "slash_menu", State: detect.Idle, Priority: 200, Region: detect.Bottom(5),
		Contains: []string{"navigate", "tab complete"},
	},
	{
		ID: "esc_cancel_footer_working", State: detect.Working, Priority: 110, Region: detect.Bottom(5),
		Regex: []string{`(?m)^\s*─{3,}\s*esc to cancel\b`},
	},
	{
		ID: "spinner_working", State: detect.Working, Priority: 100, Region: detect.Bottom(12),
		Lines: []string{`^\s*[\x{2801}-\x{28FF}]+\s+\S`},
	},
	{
		ID: "prompt_footer", State: detect.Idle, Priority: 90, Region: detect.Bottom(5),
		Lines: []string{`^\s*\? for shortcuts\b`},
	},
	// Its dialogs mark the option picked with ">".
}).WithCursors(">")
