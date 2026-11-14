// Package prompttmpl reads prompt templates and fills them in.
//
// A template is a Markdown file whose name, without .md, is the template's
// name. Front matter in YAML, between lines of three dashes, says what it
// is for, the agents it is for and the arguments it takes; the rest is the
// prompt, in which {{name}} stands for an argument's value or a built-in
// variable's. There is nothing more to the language: no functions, no
// conditions, no loops. A pair of braces around anything that is not a
// name is left as it is.
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
	Choice  Type = "choice"  // one of its options
)

// Types are the types an argument can take.
var Types = []Type{String, Text, Integer, Number, Boolean, Choice}

// Arg is an argument a template takes.
type Arg struct {
	Name        string   `yaml:"name"`
	Type        Type     `yaml:"type,omitempty"`
	Description string   `yaml:"description,omitempty"`
	Required    bool     `yaml:"required,omitempty"`
	Default     *string  `yaml:"default,omitempty"`
	Options     []string `yaml:"options,omitempty,flow"`
}

// Template is a prompt template.
type Template struct {
	Name        string `yaml:"-"`
	Path        string `yaml:"-"`
	Description string `yaml:"description,omitempty"`
	// Agents are the agents the template is for, by name or as
	// agent:profile; none for every agent.
	Agents []string `yaml:"agents,omitempty,flow"`
	Args   []Arg    `yaml:"args,omitempty"`
	Body   string   `yaml:"-"`
}

// Builtins are the variables every template may use, filled from the
// session it is sent to.
var Builtins = []string{"agent", "profile", "model", "project", "branch", "repo", "date"}

// placeholder is {{name}}, spaces allowed inside the braces.
var placeholder = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_-]*)\s*\}\}`)

var argName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// Parse reads a template named name from its file's content.
func Parse(name string, content []byte) (*Template, error) {
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
	return t, t.check()
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

// check finds what is wrong with a template as it was read, and fills in
// what an argument leaves out.
func (t *Template) check() error {
	seen := map[string]bool{}
	for i := range t.Args {
		a := &t.Args[i]
		if !argName.MatchString(a.Name) {
			return fmt.Errorf("argument %q: not a name", a.Name)
		}
		if seen[a.Name] {
			return fmt.Errorf("argument %q: given twice", a.Name)
		}
		if slices.Contains(Builtins, a.Name) {
			return fmt.Errorf("argument %q: a built-in variable has that name", a.Name)
		}
		seen[a.Name] = true
		if a.Type == "" {
			a.Type = String
			if len(a.Options) > 0 {
				a.Type = Choice
			}
		}
		if !slices.Contains(Types, a.Type) {
			return fmt.Errorf("argument %q: no type %q", a.Name, a.Type)
		}
		if a.Type == Choice && len(a.Options) == 0 {
			return fmt.Errorf("argument %q: a choice needs options", a.Name)
		}
		if a.Type != Choice && len(a.Options) > 0 {
			return fmt.Errorf("argument %q: only a choice has options", a.Name)
		}
		if a.Default != nil {
			v, err := a.convert(*a.Default)
			if err != nil {
				return fmt.Errorf("argument %q: default: %w", a.Name, err)
			}
			a.Default = &v
		}
	}
	for _, m := range placeholder.FindAllStringSubmatch(t.Body, -1) {
		if !seen[m[1]] && !slices.Contains(Builtins, m[1]) {
			return fmt.Errorf("{{%s}}: no argument or built-in variable has that name", m[1])
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
		if a.Type == Choice {
			kind = strings.Join(a.Options, "|")
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
		if e.Type == Choice {
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
	case Choice:
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
// then its arguments. It reports false for any other draft.
func ParseCall(draft string) (Call, bool) {
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
// name=value for an argument by its name. An argument not given takes its
// default, or nothing.
func (t *Template) Bind(args string) (map[string]string, error) {
	words, err := agentspec.SplitArgs(args)
	if err != nil {
		return nil, &ArgError{Kind: Unquoted}
	}
	values := map[string]string{}
	next := 0 // the argument the next positional value is for
	for _, w := range words {
		if name, v, ok := strings.Cut(w, "="); ok {
			if a := t.arg(name); a != nil {
				if _, given := values[name]; given {
					return nil, &ArgError{Kind: Twice, Arg: name}
				}
				if values[name], err = a.convert(v); err != nil {
					return nil, err
				}
				continue
			}
		}
		for next < len(t.Args) {
			if _, given := values[t.Args[next].Name]; !given {
				break
			}
			next++
		}
		if next >= len(t.Args) {
			return nil, &ArgError{Kind: Extra, Value: w}
		}
		a := &t.Args[next]
		if values[a.Name], err = a.convert(w); err != nil {
			return nil, err
		}
		next++
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

func (t *Template) arg(name string) *Arg {
	for i := range t.Args {
		if t.Args[i].Name == name {
			return &t.Args[i]
		}
	}
	return nil
}

// Fill is the template's prompt with each name in it given its value:
// an argument's from values, a built-in variable's from builtins.
func (t *Template) Fill(values, builtins map[string]string) string {
	out := placeholder.ReplaceAllStringFunc(t.Body, func(s string) string {
		name := strings.TrimSpace(s[2 : len(s)-2])
		if v, ok := values[name]; ok {
			return v
		}
		return builtins[name]
	})
	return strings.TrimRight(out, "\n")
}

// Expand is what a call of the template sends: its arguments bound and
// its prompt filled.
func (t *Template) Expand(args string, builtins map[string]string) (string, error) {
	values, err := t.Bind(args)
	if err != nil {
		return "", err
	}
	return t.Fill(values, builtins), nil
}

// Broken is a template file that did not read, and why.
type Broken struct {
	Name, Path string
	Err        error
}

// Load reads the template name from dir.
func Load(dir, name string) (*Template, error) {
	if !ValidName(name) {
		return nil, errors.New("a template's name has no spaces, slashes or colons")
	}
	path := filepath.Join(dir, name+".md")
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	t, err := Parse(name, body)
	if err != nil {
		return nil, err
	}
	t.Path = path
	return t, nil
}

// LoadDir reads the templates in dir, by name in order, and those of its
// files that did not read. A missing dir has none.
func LoadDir(dir string) ([]*Template, []Broken) {
	paths, _ := filepath.Glob(filepath.Join(dir, "*.md"))
	slices.Sort(paths)
	var out []*Template
	var broken []Broken
	for _, path := range paths {
		name := strings.TrimSuffix(filepath.Base(path), ".md")
		t, err := Load(dir, name)
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
