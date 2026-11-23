// Package prompttmpl reads prompt templates and fills them in.
//
// A template is a Markdown file whose name, without .md, is the template's
// name. Front matter in YAML, between lines of three dashes, says what it
// is for, the agents it is for and the arguments it takes; the rest is the
// prompt, written as Svelte writes markup, its values in double braces:
// {{name}} an argument's value or a built-in variable's, {{expression}}
// JavaScript's, and the blocks {#if}, {#each}, {#match}, {#snippet}, with
// {@const} and {@render}, and fenced ```js scripts (see syntax.go). A
// prompt compiles to JavaScript, run in a sandbox (compile.go, run.go).
//
//	---
//	description: Review a file
//	agents: [claude]
//	args:
//	  - name: file
//	    type: string
//	    required: true
//	  - name: depth
//	    type: integer
//	    default: 2
//	  - name: tone
//	    type: choice
//	    options: [kind, blunt]
//	---
//	Review {{file}}, {{depth}} levels deep, in a {{tone}} tone.
//	You are {{agent}} on {{model}}.
//
// It is called with its name after two slashes and its arguments after
// that, in their order or by name: //review main.go tone=blunt.
package prompttmpl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/ironpark/mako/internal/agentspec"
)

// Type is the kind of value an argument takes.
type Type string

const (
	String  Type = "string"  // any text on one line
	Text    Type = "text"    // any text, lines and all
	Integer Type = "integer" // a whole number
	Number  Type = "number"  // any number
	Boolean Type = "boolean" // true or false, yes or no
	Choice  Type = "choice"  // legacy name for select (one of its options)
	Select  Type = "select"  // one of its options
)

// HasOptions reports whether t picks one value from predefined options.
func (t Type) HasOptions() bool { return t == Choice || t == Select }

// Types are the types an argument can take in UI pickers and documentation.
var Types = []Type{String, Text, Integer, Number, Boolean, Select}

// Arg is an argument a template takes.
type Arg struct {
	Name        string   `yaml:"name"`
	Type        Type     `yaml:"type,omitempty"`
	Description string   `yaml:"description,omitempty"`
	Required    bool     `yaml:"required,omitempty"`
	Default     *string  `yaml:"default,omitempty"`
	Options     []string `yaml:"options,omitempty,flow"`
	// Rest has the last argument take the rest of a call as it is typed,
	// spaces and quotes and all, so free text needs no quotes.
	Rest bool `yaml:"rest,omitempty"`
}

// Template is a prompt template.
type Template struct {
	Name string `yaml:"-"`
	Path string `yaml:"-"` // "" for a built-in
	// Builtin is set for a template mako comes with (builtin.go).
	Builtin     bool   `yaml:"-"`
	Description string `yaml:"description,omitempty"`
	// Agents are the agents the template is for, by name or as
	// agent:profile; none for every agent.
	Agents []string `yaml:"agents,omitempty,flow"`
	Args   []Arg    `yaml:"args,omitempty"`
	Body   string   `yaml:"-"`
	// nodes is Body read, and comp what fills it in, as check found them.
	nodes []*node
	comp  *compiled
}

// Builtins are the variables every template may use, filled from the
// session it is sent to.
var Builtins = []string{"agent", "profile", "model", "project", "branch", "repo", "date"}

// namePattern is how a name is spelt: an argument's, a built-in
// variable's, or the one a {#match} matches.
const namePattern = `[A-Za-z_][A-Za-z0-9_-]*`

var argName = regexp.MustCompile(`^` + namePattern + `$`)

// Placeholder is a {{…}} in a template's body: its bytes [Start, End),
// braces and all, and what it holds: a name alone, or an expression.
type Placeholder struct {
	Start, End int
	Name, Expr string
}

// What the caret may be in, not yet closed, for an editor to complete.
const (
	CompleteName    = "name"    // {{name, a placeholder's
	CompleteSubject = "subject" // {#match name, a match's subject
	CompleteTag     = "tag"     // {#, {/ or {@, a tag begun
	CompleteArm     = "arm"     // {:, an arm or an else of the block it is in
)

// Completion is what the end of a text is in, not yet closed: its kind;
// where its { is, and where what is typed in it starts; what is typed; and
// the innermost block open there, "if", "each", "match" or "snippet", with
// a match's subject.
type Completion struct {
	Kind         string
	Brace, Start int
	Typed        string
	Open, Block  string
}

var (
	openName    = regexp.MustCompile(`\{\{\s*(` + namePattern + `)?$`)
	openSubject = regexp.MustCompile(`\{#match\s+(` + namePattern + `)?$`)
	openTag     = regexp.MustCompile(`\{([#/@][A-Za-z]*)$`)
	openArm     = regexp.MustCompile(`\{:\s*([^{}\n]*)$`)
	closing     = regexp.MustCompile(`^\s*\}\}`)
	closingOne  = regexp.MustCompile(`^\s*\}`)
)

// Complete finds what the end of before is in, not yet closed; ok is
// false when it is in none.
func Complete(before string) (c Completion, ok bool) {
	found := func(kind string, m []int) (Completion, bool) {
		c := Completion{Kind: kind, Brace: m[0], Start: len(before)}
		c.Open, c.Block = openBlock(before[:m[0]])
		if m[2] >= 0 {
			c.Start, c.Typed = m[2], before[m[2]:m[3]]
		}
		return c, true
	}
	if open, _ := openBlock(before); open == TagRaw {
		// What a raw block holds is text: all it can take is its end.
		if m := openTag.FindStringSubmatchIndex(before); m != nil && strings.HasPrefix(before[m[2]:], "/") {
			return found(CompleteTag, m)
		}
		return Completion{}, false
	}
	if m := openArm.FindStringSubmatchIndex(before); m != nil {
		if open, _ := openBlock(before[:m[0]]); open != "" && open != "snippet" {
			return found(CompleteArm, m)
		}
	}
	if m := openSubject.FindStringSubmatchIndex(before); m != nil {
		return found(CompleteSubject, m)
	}
	if m := openTag.FindStringSubmatchIndex(before); m != nil {
		return found(CompleteTag, m)
	}
	if m := openName.FindStringSubmatchIndex(before); m != nil {
		return found(CompleteName, m)
	}
	return Completion{}, false
}

// openBlock is the innermost block open at the end of s, "" for none,
// with a match's subject.
func openBlock(s string) (block, subject string) {
	var open []Tag
	for _, t := range Tags(s) {
		switch t.Kind {
		case TagIf, TagEach, TagMatch, TagSnippet, TagRaw:
			open = append(open, t)
		case TagEnd:
			if len(open) > 0 {
				open = open[:len(open)-1]
			}
		}
	}
	if len(open) == 0 {
		return "", ""
	}
	t := open[len(open)-1]
	if t.Kind == TagMatch {
		return t.Block, t.Arg
	}
	return t.Block, ""
}

// Closing is how long the }} that after begins with is, spaces before it
// and all; 0 when it begins with none.
func Closing(after string) int {
	if m := closing.FindStringIndex(after); m != nil {
		return m[1]
	}
	return 0
}

// ClosingOne is how long the } that after begins with is, spaces before it
// and all; 0 when it begins with none.
func ClosingOne(after string) int {
	if m := closingOne.FindStringIndex(after); m != nil {
		return m[1]
	}
	return 0
}

// IsName reports whether s is spelt as a name: an argument's, a built-in
// variable's; a block's subject spelt otherwise is JavaScript.
func IsName(s string) bool { return argName.MatchString(s) }

// IsBuiltin reports whether name is one of the built-in variables.
func IsBuiltin(name string) bool { return slices.Contains(Builtins, name) }

// Parse reads a template named name from its file's content.
func Parse(name string, content []byte) (*Template, error) {
	t, err := parseRaw(name, content)
	if err != nil {
		return nil, err
	}
	nodes, comp, err := t.prepare()
	if err != nil {
		return nil, err
	}
	t.nodes, t.comp = nodes, comp
	return t, nil
}

// ParseMetadata reads a template's front matter, metadata, and raw body
// without compiling expressions or instantiating a JavaScript runtime.
func ParseMetadata(name string, content []byte) (*Template, error) {
	return parseRaw(name, content)
}

func parseRaw(name string, content []byte) (*Template, error) {
	t := &Template{Name: name}
	front, body, err := split(content)
	if err != nil {
		return nil, err
	}
	if len(front) > 0 {
		dec := yaml.NewDecoder(bytes.NewReader(front))
		dec.KnownFields(true)
		if err := dec.Decode(t); err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("front matter: %w", err)
		}
	}
	t.Body = body
	return t, t.checkMetadata()
}

// split parts a file into its front matter and the body after it.
func split(content []byte) (front []byte, body string, err error) {
	s := strings.ReplaceAll(string(content), "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") {
		return nil, s, nil
	}
	rest := s[len("---\n"):]
	end := -1
	if strings.HasPrefix(rest, "---\n") || rest == "---" {
		end = 0
	} else if i := strings.Index(rest, "\n---\n"); i >= 0 {
		end = i + 1
	} else if strings.HasSuffix(rest, "\n---") {
		end = len(rest) - len("---")
	}
	if end < 0 {
		return nil, "", errors.New("front matter has no closing ---")
	}
	front = []byte(rest[:end])
	body = strings.TrimPrefix(rest[end:], "---")
	return front, strings.TrimPrefix(body, "\n"), nil
}

// checkMetadata validates front matter arguments and fills in defaults
// without parsing or compiling the template body.
func (t *Template) checkMetadata() error {
	seen := map[string]bool{}
	for i := range t.Args {
		a := &t.Args[i]
		if !argName.MatchString(a.Name) {
			return fmt.Errorf("argument %q: not a name", a.Name)
		}
		if seen[a.Name] {
			return fmt.Errorf("argument %q: given twice", a.Name)
		}
		if IsBuiltin(a.Name) {
			return fmt.Errorf("argument %q: a built-in variable has that name", a.Name)
		}
		if a.Name == "mako" {
			return fmt.Errorf("argument %q: reserved for mako host scripts", a.Name)
		}
		seen[a.Name] = true
		if a.Type == "" {
			a.Type = String
			if len(a.Options) > 0 {
				a.Type = Choice
			}
		}
		if !slices.Contains(Types, a.Type) && a.Type != Choice {
			return fmt.Errorf("argument %q: no type %q", a.Name, a.Type)
		}
		if a.Type.HasOptions() && len(a.Options) == 0 {
			return fmt.Errorf("argument %q: a %s needs options", a.Name, a.Type)
		}
		if !a.Type.HasOptions() && len(a.Options) > 0 {
			return fmt.Errorf("argument %q: only a select or choice has options", a.Name)
		}
		if a.Rest && i != len(t.Args)-1 {
			return fmt.Errorf("argument %q: only the last argument takes the rest", a.Name)
		}
		if a.Rest && a.Type != String && a.Type != Text {
			return fmt.Errorf("argument %q: only a string or text takes the rest", a.Name)
		}
		if a.Default != nil {
			v, err := a.convert(*a.Default)
			if err != nil {
				return fmt.Errorf("argument %q: default: %w", a.Name, err)
			}
			a.Default = &v
		}
	}
	return nil
}

// For reports whether the template is for agent, started with profile.
func (t *Template) For(agent, profile string) bool {
	if len(t.Agents) == 0 {
		return true
	}
	for _, a := range t.Agents {
		name, prof := agentspec.SplitTarget(a)
		if name == agent && (prof == "" || prof == profile) {
			return true
		}
	}
	return false
}

// Usage is the template's arguments as a call shows them: <name:type>
// for those required, [name:type=default] for the rest.
func (t *Template) Usage() string {
	parts := make([]string, len(t.Args))
	for i, a := range t.Args {
		kind := string(a.Type)
		if a.Type.HasOptions() {
			kind = strings.Join(a.Options, "|")
		}
		if a.Rest {
			kind += "…"
		}
		switch {
		case a.Required:
			parts[i] = "<" + a.Name + ":" + kind + ">"
		case a.Default != nil:
			parts[i] = "[" + a.Name + ":" + kind + "=" + *a.Default + "]"
		default:
			parts[i] = "[" + a.Name + ":" + kind + "]"
		}
	}
	return strings.Join(parts, " ")
}

// ErrorKind says what is wrong with a call's arguments.
type ErrorKind int

const (
	Missing  ErrorKind = iota // a required argument has no value
	Extra                     // more values than arguments
	Twice                     // an argument given twice
	BadValue                  // a value the argument's type does not take
	Unquoted                  // a quote left open
)

// ArgError is what is wrong with a call's arguments.
type ArgError struct {
	Kind  ErrorKind
	Arg   string
	Value string
	// Type and Options are what the argument takes, for BadValue.
	Type    Type
	Options []string
}

func (e *ArgError) Error() string {
	switch e.Kind {
	case Missing:
		return fmt.Sprintf("%s: required", e.Arg)
	case Extra:
		return fmt.Sprintf("%q: more values than arguments", e.Value)
	case Twice:
		return fmt.Sprintf("%s: given twice", e.Arg)
	case BadValue:
		if e.Type.HasOptions() {
			return fmt.Sprintf("%s: %q is not one of %s", e.Arg, e.Value, strings.Join(e.Options, ", "))
		}
		return fmt.Sprintf("%s: %q is not a valid %s", e.Arg, e.Value, e.Type)
	case Unquoted:
		return "a quote is not closed"
	}
	return "bad arguments"
}

// convert checks v is a value of the argument's type, and gives it as the
// prompt spells it.
func (a *Arg) convert(v string) (string, error) {
	bad := &ArgError{Kind: BadValue, Arg: a.Name, Value: v, Type: a.Type, Options: a.Options}
	switch a.Type {
	case String:
		if strings.ContainsAny(v, "\n\r") {
			return "", bad
		}
	case Integer:
		if _, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err != nil {
			return "", bad
		}
		return strings.TrimSpace(v), nil
	case Number:
		if _, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err != nil {
			return "", bad
		}
		return strings.TrimSpace(v), nil
	case Boolean:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "yes", "y", "on", "1":
			return "true", nil
		case "false", "no", "n", "off", "0":
			return "false", nil
		}
		return "", bad
	case Choice, Select:
		for _, o := range a.Options {
			if strings.EqualFold(o, strings.TrimSpace(v)) {
				return o, nil
			}
		}
		return "", bad
	}
	return v, nil
}

// Call is a template called from a draft: //name and its arguments.
type Call struct {
	Name string
	Args string // what follows the name
}

// ParseCall reads a draft that calls a template: two slashes, the name,
// then its arguments. It reports false for any other draft, including bare "//".
func ParseCall(draft string) (Call, bool) {
	c, ok := ParseDraft(draft)
	return c, ok && c.Name != ""
}

// ParseDraft reads a composer draft that calls or begins calling a template
// (including bare "//" for autocomplete prefix matching). It reports false for
// any other draft.
func ParseDraft(draft string) (Call, bool) {
	if !strings.HasPrefix(draft, "//") {
		return Call{}, false
	}
	rest := draft[2:]
	i := strings.IndexAny(rest, " \t\n")
	if i < 0 {
		return Call{Name: rest}, true
	}
	return Call{Name: rest[:i], Args: rest[i+1:]}, rest[:i] != ""
}

// Bind gives each of the template's arguments its value from a call's
// arguments, split as a shell splits words: each a value in turn, or
// name=value for an argument by its name. An argument that takes the rest
// is given what follows once those before it are, as it is typed. An
// argument not given takes its default, or nothing.
func (t *Template) Bind(args string) (map[string]string, error) {
	values := map[string]string{}
	rest := t.restArg()
	next := 0 // the argument the next positional value is for
	take := func(w string) error {
		if name, v, ok := strings.Cut(w, "="); ok {
			if a := t.arg(name); a != nil && !a.Rest {
				if _, given := values[name]; given {
					return &ArgError{Kind: Twice, Arg: name}
				}
				var err error
				values[name], err = a.convert(v)
				return err
			}
		}
		for next < len(t.Args) {
			if _, given := values[t.Args[next].Name]; !given {
				break
			}
			next++
		}
		if next >= len(t.Args) {
			return &ArgError{Kind: Extra, Value: w}
		}
		a := &t.Args[next]
		var err error
		values[a.Name], err = a.convert(w)
		next++
		return err
	}
	if rest == nil {
		words, err := agentspec.SplitArgs(args)
		if err != nil {
			return nil, &ArgError{Kind: Unquoted}
		}
		for _, w := range words {
			if err := take(w); err != nil {
				return nil, err
			}
		}
	} else {
		for t.waiting(values, rest) {
			end := wordsEnd(args)
			words, err := agentspec.SplitArgs(args[:end])
			if err != nil {
				return nil, &ArgError{Kind: Unquoted}
			}
			if len(words) == 0 {
				break
			}
			if err := take(words[0]); err != nil {
				return nil, err
			}
			args = args[end:]
		}
		if v := strings.TrimSpace(args); v != "" {
			var err error
			if values[rest.Name], err = rest.convert(v); err != nil {
				return nil, err
			}
		}
	}
	for _, a := range t.Args {
		if _, given := values[a.Name]; given {
			continue
		}
		switch {
		case a.Default != nil:
			values[a.Name] = *a.Default
		case a.Required:
			return nil, &ArgError{Kind: Missing, Arg: a.Name}
		default:
			values[a.Name] = ""
		}
	}
	return values, nil
}

// restArg is the argument that takes the rest of a call, nil for none.
func (t *Template) restArg() *Arg {
	if n := len(t.Args); n > 0 && t.Args[n-1].Rest {
		return &t.Args[n-1]
	}
	return nil
}

// waiting reports whether an argument before rest has no value yet.
func (t *Template) waiting(values map[string]string, rest *Arg) bool {
	for _, a := range t.Args {
		if a.Name == rest.Name {
			return false
		}
		if _, given := values[a.Name]; !given {
			return true
		}
	}
	return false
}

// wordsEnd is where the first word of s ends, as agentspec.SplitArgs
// splits words: at the blank after it, outside quotes, or s's end.
func wordsEnd(s string) int {
	var quote byte
	in := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
		case quote == '"':
			if c == '\\' && i+1 < len(s) && strings.IndexByte(`"\$`, s[i+1]) >= 0 {
				i++
			} else if c == '"' {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote, in = c, true
		case c == '\\' && i+1 < len(s) && strings.IndexByte(" \t\n'\"$", s[i+1]) >= 0:
			i++
			in = true
		case c == ' ' || c == '\t' || c == '\n':
			if in {
				return i
			}
		default:
			in = true
		}
	}
	return len(s)
}

func (t *Template) arg(name string) *Arg {
	for i := range t.Args {
		if t.Args[i].Name == name {
			return &t.Args[i]
		}
	}
	return nil
}

// Expand is what a call of the template sends: its arguments bound and
// its prompt filled.
func (t *Template) Expand(ctx context.Context, args string, builtins map[string]string) (string, error) {
	return t.ExpandWith(ctx, args, builtins, nil)
}

// ExpandWith expands the template with an optional host bridge for mako.* calls.
func (t *Template) ExpandWith(ctx context.Context, args string, builtins map[string]string, host Host) (string, error) {
	values, err := t.Bind(args)
	if err != nil {
		return "", err
	}
	return t.FillWith(ctx, values, builtins, host)
}

// Broken is a template file that did not read, and why.
type Broken struct {
	Name, Path string
	Err        error
}

// Load reads the template name from dir.
func Load(dir, name string) (*Template, error) {
	return loadWith(dir, name, Parse)
}

// LoadMetadata reads the template name's front matter and raw body from dir
// without compiling expressions or instantiating a JavaScript runtime.
func LoadMetadata(dir, name string) (*Template, error) {
	return loadWith(dir, name, ParseMetadata)
}

func loadWith(dir, name string, parse func(string, []byte) (*Template, error)) (*Template, error) {
	if !ValidName(name) {
		return nil, errors.New("a template's name has no spaces, slashes or colons")
	}
	path := filepath.Join(dir, name+".md")
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	t, err := parse(name, body)
	if err != nil {
		return nil, err
	}
	t.Path = path
	return t, nil
}

// LoadDir reads the templates in dir, by name in order, and those of its
// files that did not read. A missing dir has none.
func LoadDir(dir string) ([]*Template, []Broken) {
	return loadDirWith(dir, Load)
}

// LoadDirMetadata reads the templates' metadata in dir, by name in order, and
// those of its files whose front matter did not read, without compiling bodies
// or instantiating a JavaScript runtime. A missing dir has none.
func LoadDirMetadata(dir string) ([]*Template, []Broken) {
	return loadDirWith(dir, LoadMetadata)
}

func loadDirWith(dir string, load func(string, string) (*Template, error)) ([]*Template, []Broken) {
	paths, _ := filepath.Glob(filepath.Join(dir, "*.md"))
	slices.Sort(paths)
	var out []*Template
	var broken []Broken
	for _, path := range paths {
		name := strings.TrimSuffix(filepath.Base(path), ".md")
		t, err := load(dir, name)
		if err != nil {
			broken = append(broken, Broken{name, path, err})
			continue
		}
		out = append(out, t)
	}
	return out, broken
}

// Format is the file a template is kept in: its front matter, when it
// has anything to say, then its prompt. Parse reads it back as it was.
func Format(t *Template) []byte {
	front := *t
	front.Args = slices.Clone(t.Args)
	for i := range front.Args {
		if front.Args[i].Type == String {
			front.Args[i].Type = "" // the default, left unsaid
		}
	}
	body := strings.TrimRight(t.Body, "\n") + "\n"
	if t.Description == "" && len(t.Agents) == 0 && len(t.Args) == 0 {
		if strings.HasPrefix(body, "---\n") {
			return []byte("---\n---\n" + body) // so the body is not read as front matter
		}
		return []byte(body)
	}
	var buf bytes.Buffer
	buf.WriteString("---\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	_ = enc.Encode(&front)
	_ = enc.Close()
	buf.WriteString("---\n")
	buf.WriteString(body)
	return buf.Bytes()
}

// ValidName reports whether name can name a template: a file name with
// no spaces, slashes or dots to begin with.
func ValidName(name string) bool {
	return name != "" && !strings.HasPrefix(name, ".") && !strings.ContainsAny(name, " \t\n/\\:")
}
