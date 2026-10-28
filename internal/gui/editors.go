package gui

import (
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/ironpark/ggui"
	"github.com/ironpark/ggui/ui"

	"github.com/ironpark/mako/internal/agent"
)

// A file or folder on this machine opens in the code editors it has, at a
// line when one is known: from the Files tab's menu, and a file's or a
// diff's tab. A file on another node does not, as the editors here cannot
// reach it.

// editorKind is an editor mako knows how to start: its name, its command
// line program, where its installers put that program when it is not on
// the PATH, and how it is told to open a path at a line.
type editorKind struct {
	Name     string
	Programs []string
	Paths    map[string][]string // by GOOS; ~/ is the home folder, $NAME a variable
	At       func(path string, line int) []string
	Toolbox  bool // a JetBrains IDE, whose command Toolbox may have put in its own folder
}

// atColon opens path:line, as VS Code's family (with -g), Zed and Sublime
// Text take it.
func atColon(flags ...string) func(string, int) []string {
	return func(path string, line int) []string {
		if line > 0 {
			path += ":" + strconv.Itoa(line)
		}
		return append(flags, path)
	}
}

// jetBrains opens a path at a line as the JetBrains IDEs take it.
func jetBrains(path string, line int) []string {
	if line > 0 {
		return []string{"--line", strconv.Itoa(line), path}
	}
	return []string{path}
}

// vsCodeApp is where an editor built on VS Code keeps its command, in its
// macOS app and in its Windows install folder.
func vsCodeApp(app, program, winDir string) map[string][]string {
	return map[string][]string{
		"darwin": {
			"/Applications/" + app + ".app/Contents/Resources/app/bin/" + program,
			"~/Applications/" + app + ".app/Contents/Resources/app/bin/" + program,
		},
		"windows": {`$LOCALAPPDATA\Programs\` + winDir + `\bin\` + program + ".cmd"},
	}
}

var editorKinds = []editorKind{
	{Name: "VS Code", Programs: []string{"code"}, Paths: vsCodeApp("Visual Studio Code", "code", "Microsoft VS Code"), At: atColon("-g")},
	{Name: "Cursor", Programs: []string{"cursor"}, Paths: vsCodeApp("Cursor", "cursor", "cursor"), At: atColon("-g")},
	{Name: "Windsurf", Programs: []string{"windsurf"}, Paths: vsCodeApp("Windsurf", "windsurf", "Windsurf"), At: atColon("-g")},
	{Name: "Zed", Programs: []string{"zed", "zeditor"}, Paths: map[string][]string{
		"darwin": {"/Applications/Zed.app/Contents/MacOS/cli", "~/Applications/Zed.app/Contents/MacOS/cli"},
	}, At: atColon()},
	{Name: "Sublime Text", Programs: []string{"subl"}, Paths: map[string][]string{
		"darwin": {"/Applications/Sublime Text.app/Contents/SharedSupport/bin/subl"},
	}, At: atColon()},
	{Name: "GoLand", Programs: []string{"goland"}, At: jetBrains, Toolbox: true},
	{Name: "IntelliJ IDEA", Programs: []string{"idea"}, At: jetBrains, Toolbox: true},
	{Name: "WebStorm", Programs: []string{"webstorm"}, At: jetBrains, Toolbox: true},
	{Name: "PyCharm", Programs: []string{"pycharm"}, At: jetBrains, Toolbox: true},
}

// jetBrainsScripts is where JetBrains Toolbox puts its IDEs' commands.
var jetBrainsScripts = map[string]string{
	"darwin":  "Library/Application Support/JetBrains/Toolbox/scripts",
	"linux":   ".local/share/JetBrains/Toolbox/scripts",
	"windows": `AppData\Local\JetBrains\Toolbox\scripts`,
}

// editor is an editor this machine has.
type editor struct {
	editorKind
	path string
}

// found is the editors this machine has, once they have been looked for.
var found atomic.Pointer[[]editor]

// findEditors looks for the editors this machine has, off the UI
// goroutine: a menu built before they are found offers none.
func findEditors() {
	home, _ := os.UserHomeDir()
	var out []editor
	for _, k := range editorKinds {
		if p := findEditor(k, home); p != "" {
			out = append(out, editor{k, p})
		}
	}
	found.Store(&out)
}

// editors are the editors this machine has, as far as they are found.
func editors() []editor {
	if e := found.Load(); e != nil {
		return *e
	}
	return nil
}

// findEditor is where an editor's command is, "" when it is not here.
func findEditor(k editorKind, home string) string {
	var dirs []string
	if k.Toolbox {
		dirs = append(dirs, jetBrainsScripts[goruntime.GOOS])
	}
	for _, program := range k.Programs {
		if p, err := agent.Find(program, dirs...); err == nil {
			return p
		}
	}
	for _, p := range k.Paths[goruntime.GOOS] {
		if rest, ok := strings.CutPrefix(p, "~/"); ok {
			p = filepath.Join(home, rest)
		}
		if p, err := agent.Find(os.ExpandEnv(p)); err == nil { // a path: there, and runnable
			return p
		}
	}
	return ""
}

// open opens path, at line when it is more than 0, in the editor. It
// returns once the editor has the request.
func (e editor) open(path string, line int) error {
	cmd := exec.Command(e.path, e.At(path, line)...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() // the command hands the path on and exits; reap it
	return nil
}

// editorItems are the menu items opening path on node in each editor this
// machine has, at the line line gives as one is picked (nil for none): none
// for a path on another node. A failure goes to fail.
func (m *model) editorItems(node, path string, line func() int, fail func(string)) []ggui.Widget {
	if !m.onThisMachine(node) {
		return nil
	}
	var items []ggui.Widget
	for _, e := range editors() {
		items = append(items, ui.MenuItem(Tf("Open in %s", e.Name), func() {
			at := 0
			if line != nil {
				at = line()
			}
			if err := e.open(path, at); err != nil && fail != nil {
				fail(err.Error())
			}
		}))
	}
	return items
}
