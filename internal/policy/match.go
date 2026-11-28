package policy

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ironpark/mako/internal/protocol"
)

// Call is one tool call an agent asks approval for, as rules read it.
type Call struct {
	Agent string // the agent's name, as agentspec names it
	Tool  string // the tool's name, as the agent calls it
	Kind  string // what the call does, one of protocol's Tool kinds; "" for none known
	// Input is the call's arguments, whole: in the form protocol gives
	// Kind, or as the agent wrote them for a call of no kind.
	Input map[string]any
	// Dir is the directory the call's relative paths are of, its
	// session's project; "" for none known.
	Dir string
}

// toolKinds are the kinds of call a rule's tool names, whatever each agent
// calls its tools, by the name in lower case: a kind's own name, and the
// name Claude Code gives its tool of the kind. As in Claude Code's own
// permission rules, Edit names every kind that changes a file and Read
// every kind that reads them, so a rule for Bash or Edit holds for Codex's
// exec_command and apply_patch, agy's run_command and replace_file_content,
// and OpenCode's interactive_bash too. Write names, besides, every call
// that makes a file; see makesFile.
var toolKinds = map[string][]string{
	"bash":                 {protocol.ToolShell},
	protocol.ToolShell:     {protocol.ToolShell},
	"edit":                 {protocol.ToolEdit, protocol.ToolMultiEdit, protocol.ToolWrite, protocol.ToolPatch},
	protocol.ToolMultiEdit: {protocol.ToolMultiEdit},
	protocol.ToolWrite:     {protocol.ToolWrite},
	protocol.ToolPatch:     {protocol.ToolPatch},
	"apply_patch":          {protocol.ToolPatch},
	"read":                 {protocol.ToolRead, protocol.ToolSearch},
	protocol.ToolSearch:    {protocol.ToolSearch},
	"grep":                 {protocol.ToolSearch},
	"glob":                 {protocol.ToolSearch},
	"webfetch":             {protocol.ToolFetch},
	protocol.ToolFetch:     {protocol.ToolFetch},
	protocol.ToolWebSearch: {protocol.ToolWebSearch},
	protocol.ToolTask:      {protocol.ToolTask},
	"agent":                {protocol.ToolTask},
}

// toolMatches says the tool a rule names holds for call: by the call's
// tool name, a glob of names, or the call's kind.
func toolMatches(tool string, call Call) bool {
	if matchGlobFold(tool, call.Tool) {
		return true
	}
	name := strings.ToLower(strings.TrimSpace(tool))
	return slices.Contains(toolKinds[name], call.Kind) || name == protocol.ToolWrite && makesFile(call)
}

// makesFile says call can make a file that was not there: an edit from no
// text, as Claude Code's Edit makes a file of, or a patch that adds a file
// or moves one.
func makesFile(call Call) bool {
	switch call.Kind {
	case protocol.ToolEdit:
		return str(call.Input, "old") == ""
	case protocol.ToolMultiEdit:
		edits, _ := call.Input["edits"].([]any)
		return len(edits) == 0 || slices.ContainsFunc(edits, func(e any) bool {
			m, _ := e.(map[string]any)
			return str(m, "old") == ""
		})
	case protocol.ToolPatch:
		return readPatch(str(call.Input, "patch")).makes
	}
	return false
}

// subjects are what rules read of a call.
type subjects struct {
	// allow are what an allow pattern must match, each of them; ok is
	// false for a call an allow pattern cannot vouch for, as a command
	// line that substitutes or redirects.
	allow []string
	ok    bool
	// deny are what a deny pattern may match, any of them.
	deny []string
}

// patternMatches reports whether r's pattern holds for a call of subjects
// s. A deny rule holds when its pattern matches anything the call acts on;
// an allow rule only when it matches everything the call acts on, so a
// call cannot carry more than the rule vouches for past it.
func patternMatches(r Rule, s subjects) bool {
	match := func(v string) bool { return matchGlobFold(r.Pattern, v) }
	if r.Action == ActionDeny {
		return slices.ContainsFunc(s.deny, match)
	}
	return s.ok && len(s.allow) > 0 && !slices.ContainsFunc(s.allow, func(v string) bool { return !match(v) })
}

// readCall is what rules read of call, untruncated: the command it runs and
// each command in it, every path it touches, the URL it fetches. A call of
// no kind known, or of a kind with no fields of what it acts on, acts on
// every string its arguments hold.
func readCall(call Call) subjects {
	in := call.Input
	pl := newPlaces(call.Dir)
	var s subjects
	both := func(v ...string) {
		v = present(v...)
		s.allow, s.deny = append(s.allow, v...), append(s.deny, v...)
	}
	path := func(p string) {
		canon, all := pl.forms(p)
		s.allow, s.deny = append(s.allow, canon...), append(s.deny, all...)
	}
	s.ok = true
	switch call.Kind {
	case protocol.ToolShell:
		line := strings.TrimSpace(str(in, "command"))
		cmds, simple := shellCommands(line)
		s.allow, s.ok = cmds, simple
		s.deny = append(present(line), cmds...)
		// A command line run elsewhere is not the one a rule vouched for
		// in the session's project.
		if cwd := strings.TrimSpace(str(in, "cwd")); cwd != "" {
			canon, all := pl.forms(cwd)
			s.ok = s.ok && slices.Equal(canon, []string{"."})
			s.deny = append(s.deny, all...)
		}
	case protocol.ToolPatch:
		blob := str(in, "patch")
		p := readPatch(blob)
		for _, f := range p.files {
			path(f)
		}
		// Each line that might name a file is read as one, by an allow
		// pattern, should the agent's patch tool read it as one.
		for _, f := range p.loose {
			canon, _ := pl.forms(f)
			s.allow = append(s.allow, canon...)
		}
		s.ok = len(p.files) > 0
		if len(p.files) == 0 {
			s.deny = present(blob)
		}
	case protocol.ToolEdit, protocol.ToolMultiEdit, protocol.ToolWrite, protocol.ToolRead:
		path(str(in, "path"))
	case protocol.ToolSearch:
		// A search reads the files under its path, the project without one,
		// that its glob, or its pattern for a search by name, picks.
		root := strings.TrimSpace(str(in, "path"))
		if root == "" {
			root = "."
		}
		path(root)
		for _, g := range present(str(in, "glob"), str(in, "pattern")) {
			canon, all := pl.forms(pl.join(root, g))
			s.allow, s.deny = append(s.allow, canon...), append(s.deny, append(all, g)...)
			// A glob that climbs out of its path, as **/.. may, is read
			// by no allow pattern.
			if slices.Contains(strings.Split(filepath.ToSlash(g), "/"), "..") {
				s.ok = false
			}
		}
	case protocol.ToolFetch:
		both(str(in, "url"))
	case protocol.ToolWebSearch:
		both(str(in, "query"))
	case protocol.ToolTask:
		both(str(in, "description"), str(in, "prompt"))
	default:
		both(stringsIn(in, nil)...)
	}
	s.allow, s.deny = unique(s.allow), unique(s.deny)
	return s
}

// places places the paths a call names against its directory.
type places struct {
	dirs []string // the directory, and the one its links resolve to
}

func newPlaces(dir string) places {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return places{}
	}
	dir = filepath.Clean(dir)
	pl := places{dirs: []string{dir}}
	if real, err := filepath.EvalSymlinks(dir); err == nil && real != dir {
		pl.dirs = append(pl.dirs, real)
	}
	return pl
}

// forms are the forms of p, a path a call names, that rules read: canon,
// each of which an allow pattern must match, are p cleaned, relative to
// the call's directory when it is in it, and the same of the file p comes
// to through links; all, any of which a deny pattern may match, are these,
// and p absolute and as written.
func (pl places) forms(p string) (canon, all []string) {
	p = strings.TrimSpace(p)
	if p == "" {
		return nil, nil
	}
	abs := filepath.Clean(filepath.FromSlash(p))
	if !filepath.IsAbs(abs) && len(pl.dirs) > 0 {
		abs = filepath.Join(pl.dirs[0], abs)
	}
	canon = []string{pl.rel(abs)}
	all = []string{canon[0], filepath.ToSlash(abs), p}
	if filepath.IsAbs(abs) {
		if real := resolve(abs); real != abs {
			canon = append(canon, pl.rel(real))
			all = append(all, pl.rel(real), filepath.ToSlash(real))
		}
	}
	return unique(canon), unique(all)
}

// rel is abs relative to the call's directory, in slashes, when it is in
// it; else abs.
func (pl places) rel(abs string) string {
	for _, d := range pl.dirs {
		if r, err := filepath.Rel(d, abs); err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(r)
		}
	}
	return filepath.ToSlash(abs)
}

// join is glob g under root, as a search reads it: g itself when it is
// absolute.
func (pl places) join(root, g string) string {
	if filepath.IsAbs(g) || strings.HasPrefix(g, "~") {
		return g
	}
	return filepath.Join(filepath.FromSlash(root), filepath.FromSlash(g))
}

// resolve is abs with the links of its longest part that exists resolved,
// a link to no file among them: a file written through one is made where
// it points.
func resolve(abs string) string {
	for range 40 {
		dir, rest := abs, ""
		for {
			if real, err := filepath.EvalSymlinks(dir); err == nil {
				return filepath.Join(real, rest)
			}
			if fi, err := os.Lstat(dir); err == nil {
				to, err := os.Readlink(dir)
				if fi.Mode()&os.ModeSymlink == 0 || err != nil {
					return abs
				}
				if !filepath.IsAbs(to) {
					to = filepath.Join(filepath.Dir(dir), to)
				}
				abs = filepath.Join(to, rest)
				break
			}
			up := filepath.Dir(dir)
			if up == dir {
				return abs
			}
			dir, rest = up, filepath.Join(filepath.Base(dir), rest)
		}
	}
	return abs
}

// patch is what rules read of a patch.
type patch struct {
	files []string // the files its headers name
	// loose are the lines that might name a file to a reader less strict
	// than readPatch: one that trims them, or that skips no hunk.
	loose []string
	makes bool // it adds a file, or moves one
}

// codexOps are the headers of Codex's patch format, each followed by the
// file it names.
var codexOps = []string{"*** Add File: ", "*** Update File: ", "*** Delete File: ", "*** Move to: "}

// readPatch reads the files patch touches from its headers: in Codex's
// format, which OpenCode's apply_patch takes too, and as a unified diff, as
// git and pi write them. A line in a hunk is the file's text, never a
// header, however it reads.
func readPatch(text string) patch {
	var p patch
	const (
		between = iota // between hunks, where a header may come
		added          // in an added file's text, each line +
		updated        // in an updated file's chunks
		unified        // in a unified diff's hunk, of old and new lines left
	)
	state := between
	var old, cur int
	header := func(f string, makes bool) {
		if f = strings.TrimSpace(f); f != "" {
			p.files = append(p.files, f)
			p.makes = p.makes || makes
		}
	}
	for raw := range strings.Lines(text) {
		line := strings.TrimRight(raw, "\r\n")
		if t := strings.TrimSpace(line); slices.ContainsFunc(codexOps, func(op string) bool { return strings.HasPrefix(t, op) }) ||
			strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") {
			for _, op := range codexOps {
				if f, ok := strings.CutPrefix(t, op); ok {
					p.loose = append(p.loose, f)
				}
			}
			if len(line) > 4 && (line[:4] == "--- " || line[:4] == "+++ ") {
				if f := diffPath(line[4:]); f != "/dev/null" {
					p.loose = append(p.loose, f)
				}
			}
		}
		switch state {
		case added:
			if strings.HasPrefix(line, "+") {
				continue
			}
			state = between
		case updated:
			// Codex ends a file's chunks at a line that starts with ***,
			// and reads one that starts with a space as context.
			if f, ok := strings.CutPrefix(line, "*** Move to: "); ok {
				header(f, true)
				continue
			}
			if !strings.HasPrefix(line, "***") || strings.HasPrefix(line, "*** End of File") {
				continue
			}
			state = between
		case unified:
			switch {
			case strings.HasPrefix(line, `\`):
			case strings.HasPrefix(line, "-"):
				old--
			case strings.HasPrefix(line, "+"):
				cur--
			default:
				old, cur = old-1, cur-1
			}
			if old <= 0 && cur <= 0 {
				state = between
			}
			continue
		}
		// Between hunks: Codex reads its headers trimmed, as it reads
		// them; a unified diff's start at the line's start.
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "*** Add File: "):
			header(t[len("*** Add File: "):], true)
			state = added
		case strings.HasPrefix(t, "*** Update File: "):
			header(t[len("*** Update File: "):], false)
			state = updated
		case strings.HasPrefix(t, "*** Delete File: "):
			header(t[len("*** Delete File: "):], false)
		case strings.HasPrefix(line, "--- "):
			if f := diffPath(line[4:]); f == "/dev/null" {
				p.makes = true
			} else {
				header(f, false)
			}
		case strings.HasPrefix(line, "+++ "):
			if f := diffPath(line[4:]); f != "/dev/null" {
				header(f, false)
			}
		case strings.HasPrefix(line, "diff --git "):
			a, b := gitDiffPaths(line[len("diff --git "):])
			header(a, false)
			header(b, false)
		case strings.HasPrefix(line, "rename from "), strings.HasPrefix(line, "copy from "):
			_, f, _ := strings.Cut(line, " from ")
			header(diffPath(f), false)
		case strings.HasPrefix(line, "rename to "), strings.HasPrefix(line, "copy to "):
			_, f, _ := strings.Cut(line, " to ")
			header(diffPath(f), true)
		case strings.HasPrefix(line, "new file mode"):
			p.makes = true
		case strings.HasPrefix(line, "@@ -"):
			if o, c, ok := hunkLines(line); ok {
				old, cur, state = o, c, unified
			}
		}
	}
	p.files, p.loose = unique(p.files), unique(p.loose)
	return p
}

// diffPath is the file a unified diff's header names: unquoted, its time
// stamp and git's a/ or b/ gone.
func diffPath(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, `"`) {
		if u, err := strconv.Unquote(s); err == nil {
			s = u
		}
	} else if name, _, ok := strings.Cut(s, "\t"); ok {
		s = name
	}
	if s == "/dev/null" {
		return s
	}
	for _, pre := range []string{"a/", "b/"} {
		if f, ok := strings.CutPrefix(s, pre); ok {
			return f
		}
	}
	return s
}

// gitDiffPaths are the two files a diff --git line names.
func gitDiffPaths(s string) (a, b string) {
	if i := strings.LastIndex(s, " b/"); i >= 0 {
		return diffPath(s[:i]), diffPath(s[i+1:])
	}
	return diffPath(s), ""
}

// hunkLines are how many old and new lines a unified diff's hunk header
// says follow.
func hunkLines(line string) (old, cur int, ok bool) {
	f := strings.Fields(line)
	if len(f) < 3 || !strings.HasPrefix(f[1], "-") || !strings.HasPrefix(f[2], "+") {
		return 0, 0, false
	}
	count := func(r string) (int, bool) {
		_, n, found := strings.Cut(r, ",")
		if !found {
			return 1, true
		}
		v, err := strconv.Atoi(n)
		return v, err == nil && v >= 0
	}
	old, ok1 := count(f[1][1:])
	cur, ok2 := count(f[2][1:])
	return old, cur, ok1 && ok2 && old+cur > 0
}

// str is in[key] when it is a string.
func str(in map[string]any, key string) string {
	s, _ := in[key].(string)
	return s
}

// present are those of ss that are not blank, trimmed.
func present(ss ...string) []string {
	var out []string
	for _, s := range ss {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// unique is ss without its repeats, in its order.
func unique(ss []string) []string {
	out := ss[:0:0]
	for _, s := range ss {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// stringsIn appends to out every string v holds, at any depth, a map's in
// the order of its keys.
func stringsIn(v any, out []string) []string {
	switch v := v.(type) {
	case string:
		out = append(out, present(v)...)
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			out = stringsIn(v[k], out)
		}
	case []any:
		for _, e := range v {
			out = stringsIn(e, out)
		}
	}
	return out
}
