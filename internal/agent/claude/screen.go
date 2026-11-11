package claude

import (
	"slices"

	"github.com/ironpark/mako/internal/detect"
)

// ScreenRules read what Claude Code shows: its title's spinner while it
// works, the dialogs it asks in, the prompt box it waits at. They follow
// herdr's rules for Claude (github.com/herdrdev/herdr, Apache-2.0).
func (*Agent) ScreenRules() *detect.Rules { return screenRules }

// permissionNot are what a permission dialog shows, which a working line
// above one does not outweigh.
var permissionNot = []detect.Gate{
	{Contains: []string{"do you want to proceed?"}},
	{Contains: []string{"esc to cancel"}},
	{Contains: []string{"waiting for permission"}},
	{Contains: []string{"do you want to allow this connection?"}},
	{Contains: []string{"tab to amend"}},
	{Contains: []string{"ctrl+e to explain"}},
}

// navigate are the ways a picker's footer says to move through it.
var navigate = []detect.Gate{
	{Contains: []string{"tab/arrow keys to navigate"}},
	{Contains: []string{"arrow keys to navigate"}},
	{Contains: []string{"arrows to navigate"}},
	{Contains: []string{"↑/↓ to navigate"}},
	{Contains: []string{"↑↓ to navigate"}},
}

// askYes are an older dialog's question, with a yes or a choice marked.
var askYes = []detect.Gate{
	{Contains: []string{"do you want to"}, Any: []detect.Gate{{Contains: []string{"yes"}}, {Contains: []string{"❯"}}}},
	{Contains: []string{"would you like to"}, Any: []detect.Gate{{Contains: []string{"yes"}}, {Contains: []string{"❯"}}}},
}

// yesNo are a dialog's numbered choices, the selected one marked "❯".
var yesNo = []detect.Gate{
	{Lines: []string{`(?i)^\s*❯?\s*1\.\s*yes\b`}},
	{Lines: []string{`(?i)^\s*❯?\s*2\.\s*yes\b`}},
	{Lines: []string{`(?i)^\s*❯?\s*2\.\s*no\b`}},
	{Lines: []string{`(?i)^\s*❯?\s*3\.\s*no\b`}},
}

// transcriptViewer is the detailed transcript (ctrl+o) over the prompt.
var transcriptViewer = detect.Rule{
	ID: "transcript_viewer", Priority: 1000, Region: detect.Bottom(3), Hold: true,
	Gate: detect.Gate{
		Contains: []string{"showing detailed transcript"},
		Any: []detect.Gate{
			{Contains: []string{"ctrl+o", "to toggle"}},
			{Contains: []string{"ctrl+e", "show all"}},
			{Contains: []string{"ctrl+e", "collapse"}},
			{Contains: []string{"↑↓ scroll"}},
			{Contains: []string{"? for shortcuts"}},
		},
	},
}

// promptBoxRule is the prompt box, waiting for the user.
var promptBoxRule = detect.Rule{
	ID: "prompt_box", State: detect.Idle, Priority: 950, Region: detect.PromptBox,
	Gate: detect.Gate{
		Lines: []string{`^\s*❯`},
		Not: []detect.Gate{
			{Contains: []string{"enter to select"}},
			{Contains: []string{"esc to cancel"}},
			{Contains: []string{"tab/arrow keys"}},
			{Contains: []string{"arrow keys to navigate"}},
			{Contains: []string{"↑/↓ to navigate"}},
		},
	},
}

var screenRules = detect.MustCompile([]detect.Rule{
	{
		// Braille is the spinner of 2.1.227 and before; half circles of after.
		ID: "title_working", State: detect.Working, Priority: 1100, Region: detect.Title,
		Gate: detect.Gate{Regex: []string{`^[\x{2800}-\x{28FF}\x{25D0}-\x{25D3}] `}},
	},
	{
		ID: "live_turn", State: detect.Working, Priority: 970, Region: detect.Bottom(12),
		Gate: detect.Gate{Any: []detect.Gate{
			{Lines: []string{`^\s*[⏸⏵].*esc to interrupt(?:\s|·|$)`}},
			{Lines: []string{`^\s*[\x{002A}\x{00B7}\x{2722}\x{2733}\x{2736}\x{273B}\x{273D}]\s+\S.*…(?:\s+\(\d+[smh](?:\s|·)|\s*$)`}},
		}},
	},
	{
		ID: "background_agents", State: detect.Working, Priority: 965, Region: detect.LastAbovePromptBox,
		Gate: detect.Gate{Lines: []string{`^\s*[\x{002A}\x{00B7}\x{2722}\x{2736}\x{273B}\x{273D}]\s+Waiting for [1-9]\d* background agents? to finish\s*$`}},
	},
	{
		// An activity line at column zero; prompt text the user wrote,
		// indented under its marker, cannot pass for one.
		ID: "background_mcp_tasks", State: detect.Working, Priority: 965, Region: detect.Bottom(12),
		Gate: detect.Gate{
			Regex: []string{`(?m)^[\x{002A}\x{00B7}\x{2722}\x{2736}\x{273B}\x{273D}][ \t]+\S[^\n]*?(?:\n[ \t]+[^\n]*?){0,3}·(?:[ \t]+|\n[ \t]*)[1-9]\d*(?:[ \t]+|\n[ \t]*)MCP(?:[ \t]+|\n[ \t]*)tasks?(?:[ \t]+|\n[ \t]*)still(?:[ \t]+|\n[ \t]*)running[ \t]*$`},
			Not:   permissionNot,
		},
	},
	{
		ID: "btw_overlay", State: detect.Working, Priority: 975, Region: detect.Bottom(5),
		Gate: detect.Gate{Lines: []string{`^\s*/btw(?:\s|$)`, `(?i)esc to close\s*$`}},
	},
	transcriptViewer,
	{
		ID: "form", State: detect.Blocked, Priority: 980, Region: detect.AfterRule,
		Gate: detect.Gate{
			Contains: []string{"esc to cancel"},
			Any: []detect.Gate{
				{Contains: []string{"enter to confirm"}},
				{Contains: []string{"enter to select"}, Any: navigate},
			},
		},
	},
	{
		// A named session draws its name's divider under the form's footer,
		// and may draw its task list under that.
		ID: "form_named_session", State: detect.Blocked, Priority: 980, Region: detect.Whole,
		Gate: detect.Gate{Regex: []string{`(?i)(?:^|\n)[ \t]*enter\s+to\s+(?:confirm|select\s+·\s+(?:(?:tab/arrow|arrow)\s+keys|arrows|↑/↓|↑↓)\s+to\s+navigate)\s+·\s+esc\s+to\s+cancel[ \t]*\n(?:[ \t]*\n)*[ \t]*─{3,}[^\n]*(?:\n(?:[ \t]*\n)*[ \t]+\d+ tasks? \(\d+ done, (?:\d+ in progress, )?\d+ open\)(?:\n[ \t]+[◻◼✔][ \t]+[^\n]+)+(?:\n[ \t]+…[ \t]+\+\d+[ \t]+pending)?)?(?:\n[ \t]*)*\z`}},
	},
	{
		ID: "dynamic_workflow", State: detect.Blocked, Priority: 980, Region: detect.Whole,
		Gate: detect.Gate{Contains: []string{"run a dynamic workflow?", "esc to cancel"}},
	},
	{
		// An MCP server's request for input has Accept and Decline, and no
		// Enter in its footer.
		ID: "mcp_elicitation", State: detect.Blocked, Priority: 980, Region: detect.Whole,
		Gate: detect.Gate{
			Contains: []string{"esc to cancel"},
			Lines:    []string{`(?i)^\s*MCP server ["\x{201C}].+["\x{201D}] requests your input\s*$`},
			Any: []detect.Gate{
				{Lines: []string{`^\s*\x{276F}?\s*Accept\b`}},
				{Lines: []string{`^\s*\x{276F}?\s*Decline\b`}},
			},
		},
	},
	promptBoxRule,
	{
		ID: "model_picker", Priority: 900, Region: detect.Whole, Hold: true,
		Gate: detect.Gate{
			Contains: []string{"select model", "enter to set as default", "esc to cancel"},
			Not:      []detect.Gate{{Contains: []string{"do you want to proceed?"}}, {Contains: []string{"enter to select"}}},
		},
	},
	{
		// The selected choice is marked "❯": the resting dialog is
		// "❯ 1. Yes / 2. Yes, and don't ask again / 3. No".
		ID: "bash_permission", State: detect.Blocked, Priority: 850, Region: detect.Whole,
		Gate: detect.Gate{
			Contains: []string{"do you want to proceed?"},
			Any: []detect.Gate{
				{Contains: []string{"bash command"}},
				{Contains: []string{"bash("}},
				{Contains: []string{"contains expansion"}},
				{Contains: []string{"tab to amend"}},
				{Contains: []string{"ctrl+e to explain"}},
			},
			All: []detect.Gate{{Any: append([]detect.Gate{{Lines: []string{`(?i)^\s*❯?\s*yes\b`}}}, yesNo...)}},
		},
	},
	{
		ID: "permission", State: detect.Blocked, Priority: 840, Region: detect.AfterRule,
		Gate: detect.Gate{
			Contains: []string{"do you want to proceed?", "esc to cancel"},
			Any:      yesNo,
		},
	},
	{
		// Dialogs of older versions, unless an empty prompt is below them.
		ID: "older_dialog", State: detect.Blocked, Priority: 300, Region: detect.Whole, Suggests: true,
		Gate: detect.Gate{
			Any: slices.Concat(askYes, []detect.Gate{
				{Contains: []string{"waiting for permission"}},
				{Contains: []string{"do you want to allow this connection?"}},
				{Contains: []string{"tab to amend"}},
				{Contains: []string{"ctrl+e to explain"}},
				{Contains: []string{"do you want to proceed?", "esc to cancel"}},
				{Contains: []string{"review your answers"}},
				{Contains: []string{"skip interview and plan immediately"}},
			}),
			Not: []detect.Gate{{Regex: []string{`(?m)^\s*❯\s*$`}}},
		},
	},
	{
		ID: "title_idle", State: detect.Idle, Priority: 250, Region: detect.Title,
		Gate: detect.Gate{Regex: []string{`^\x{2733} `}},
	},
})
