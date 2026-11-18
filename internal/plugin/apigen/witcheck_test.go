package apigen_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ironpark/mako/internal/plugin/apigen"
)

// No WIT parser is to be had from Go without cgo (wasm-tools is Rust),
// so the WIT generated is checked here by a small parser of the part of
// WIT the generator writes: its names are WIT identifiers, every type it
// names is a builtin or declared and in scope, and the world imports
// interfaces that exist. It is not a full validation of WIT.

var witToken = regexp.MustCompile(`[a-z]+:[a-z]+@[0-9.]+|%?[a-z][a-z0-9]*(?:-[a-z][a-z0-9]*)*|->|[{}();:,<>._]`)

type witParser struct {
	toks []string
	i    int
	err  error
}

func (p *witParser) peek() string {
	if p.i < len(p.toks) {
		return p.toks[p.i]
	}
	return ""
}

func (p *witParser) next() string {
	t := p.peek()
	p.i++
	return t
}

func (p *witParser) expect(want string) {
	if got := p.next(); got != want && p.err == nil {
		p.err = fmt.Errorf("token %d: want %q, got %q", p.i, want, got)
	}
}

func (p *witParser) ident() string {
	t := p.next()
	if !regexp.MustCompile(`^%?[a-z][a-z0-9]*(-[a-z][a-z0-9]*)*$`).MatchString(t) && p.err == nil {
		p.err = fmt.Errorf("token %d: %q is not a WIT identifier", p.i, t)
	}
	return strings.TrimPrefix(t, "%")
}

var witBuiltins = map[string]bool{"string": true, "bool": true, "u8": true, "u32": true, "s32": true, "f32": true, "f64": true}

// typ reads a type, and calls use with each name it refers to.
func (p *witParser) typ(use func(string)) {
	switch t := p.peek(); t {
	case "list", "option":
		p.next()
		p.expect("<")
		p.typ(use)
		p.expect(">")
	case "tuple":
		p.next()
		p.expect("<")
		p.typ(use)
		for p.peek() == "," {
			p.next()
			p.typ(use)
		}
		p.expect(">")
	case "result":
		p.next()
		p.expect("<")
		if p.peek() == "_" {
			p.next()
		} else {
			p.typ(use)
		}
		p.expect(",")
		p.typ(use)
		p.expect(">")
	default:
		name := p.ident()
		if !witBuiltins[name] {
			use(name)
		}
	}
}

func TestWITIsWellFormed(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(root, apigen.OutDir, "mako.wit"))
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), "//") {
			lines = append(lines, l)
		}
	}
	src := strings.Join(lines, "\n")
	p := &witParser{toks: witToken.FindAllString(src, -1)}
	// Nothing but tokens and space may be left once they are taken out.
	if rest := strings.TrimSpace(witToken.ReplaceAllString(src, "")); rest != "" {
		t.Fatalf("characters no token takes: %q", rest)
	}
	p.expect("package")
	if pkg := p.next(); pkg != apigen.WITPackage {
		t.Fatalf("package %q, want %q", pkg, apigen.WITPackage)
	}
	p.expect(";")
	types := map[string]bool{}
	var refs []string
	interfaces := map[string]bool{}
	for p.err == nil && p.peek() != "" {
		switch kw := p.next(); kw {
		case "interface":
			name := p.ident()
			if interfaces[name] {
				t.Fatalf("interface %s twice", name)
			}
			interfaces[name] = true
			p.expect("{")
			scope := map[string]bool{}
			for p.err == nil && p.peek() != "}" {
				switch p.peek() {
				case "record":
					p.next()
					rec := p.ident()
					types[rec] = true
					p.expect("{")
					for p.err == nil && p.peek() != "}" {
						p.ident()
						p.expect(":")
						p.typ(func(n string) { refs = append(refs, n) })
						p.expect(",")
					}
					p.expect("}")
				case "enum":
					p.next()
					types[p.ident()] = true
					p.expect("{")
					for p.err == nil && p.peek() != "}" {
						p.ident()
						p.expect(",")
					}
					p.expect("}")
				case "use":
					p.next()
					if from := p.ident(); from != "types" {
						t.Fatalf("%s uses %s, not types", name, from)
					}
					p.expect(".")
					p.expect("{")
					for {
						n := p.ident()
						scope[n] = true
						refs = append(refs, n)
						if p.peek() != "," {
							break
						}
						p.next()
					}
					p.expect("}")
					p.expect(";")
				default:
					fn := p.ident()
					p.expect(":")
					p.expect("func")
					p.expect("(")
					if p.peek() != ")" {
						p.ident()
						p.expect(":")
						p.typ(func(n string) {
							if !scope[n] {
								t.Errorf("%s.%s names %s, which is not in scope", name, fn, n)
							}
						})
					}
					p.expect(")")
					p.expect("->")
					p.typ(func(n string) {
						if !scope[n] {
							t.Errorf("%s.%s returns %s, which is not in scope", name, fn, n)
						}
					})
					p.expect(";")
				}
			}
			p.expect("}")
		case "world":
			p.ident()
			p.expect("{")
			for p.err == nil && p.peek() != "}" {
				p.expect("import")
				if n := p.ident(); !interfaces[n] {
					t.Errorf("the world imports %s, which is no interface", n)
				}
				p.expect(";")
			}
			p.expect("}")
		default:
			t.Fatalf("unexpected %q at the top level", kw)
		}
	}
	if p.err != nil {
		t.Fatal(p.err)
	}
	for _, r := range refs {
		if !types[r] {
			t.Errorf("%s is named but not declared", r)
		}
	}
	if len(interfaces) < 2 {
		t.Fatalf("only %d interfaces", len(interfaces))
	}
}
