package apigen

import (
	"fmt"
	"strings"
)

// WITPackage is the WIT package the Host API is, at the manifest's api
// version.
const WITPackage = "mako:plugin@1.0.0"

// genWIT writes the Host API as WIT: the messages and enums as records and
// enums of an interface of types, each service as an interface of
// functions, and a world importing them all, which a plugin's component
// targets. A function takes its request and returns its response or an
// error's message; an empty message is no parameter, or no value.
func genWIT(a *api) []byte {
	var b strings.Builder
	b.WriteString("// " + header + "\n\n")
	fmt.Fprintf(&b, "package %s;\n\n", WITPackage)

	b.WriteString("/// The records and enums the Host API's functions take and give.\ninterface types {\n")
	for i, e := range a.enums {
		if i > 0 {
			b.WriteString("\n")
		}
		prefixed(&b, "  ", "/// ", e.doc)
		fmt.Fprintf(&b, "  enum %s {\n", witIdent(kebab(e.name)))
		for _, v := range e.values {
			fmt.Fprintf(&b, "    %s,\n", witIdent(enumCase(e.name, v)))
		}
		b.WriteString("  }\n")
	}
	first := len(a.enums) == 0
	for _, m := range a.messages {
		if len(m.fields) == 0 {
			continue // an empty record is not WIT; see genWIT
		}
		if !first {
			b.WriteString("\n")
		}
		first = false
		prefixed(&b, "  ", "/// ", m.doc)
		fmt.Fprintf(&b, "  record %s {\n", witIdent(kebab(m.name)))
		for _, f := range m.fields {
			prefixed(&b, "    ", "/// ", f.doc)
			fmt.Fprintf(&b, "    %s: %s,\n", witIdent(snakeToKebab(f.name)), witType(f))
		}
		b.WriteString("  }\n")
	}
	b.WriteString("}\n")

	for _, s := range a.services {
		b.WriteString("\n")
		prefixed(&b, "", "/// ", s.doc)
		fmt.Fprintf(&b, "interface %s {\n", witIdent(kebab(s.short)))
		if uses := s.uses(); len(uses) > 0 {
			idents := make([]string, len(uses))
			for i, u := range uses {
				idents[i] = witIdent(u)
			}
			fmt.Fprintf(&b, "  use types.{%s};\n\n", strings.Join(idents, ", "))
		}
		for i, m := range s.methods {
			if i > 0 {
				b.WriteString("\n")
			}
			doc := m.doc
			if m.perm != PermNone {
				if doc != "" {
					doc += "\n"
				}
				doc += "Needs the permission " + m.perm + "."
			}
			prefixed(&b, "  ", "/// ", doc)
			params := ""
			if len(m.in.fields) > 0 {
				params = "req: " + witIdent(kebab(m.in.name))
			}
			ok := "_"
			if len(m.out.fields) > 0 {
				ok = witIdent(kebab(m.out.name))
			}
			fmt.Fprintf(&b, "  %s: func(%s) -> result<%s, string>;\n", witIdent(kebab(m.name)), params, ok)
		}
		b.WriteString("}\n")
	}

	b.WriteString("\n/// What a plugin's component imports: the whole Host API. The host\n")
	b.WriteString("/// refuses at run time the calls the plugin has no permission for.\nworld plugin {\n")
	for _, s := range a.services {
		fmt.Fprintf(&b, "  import %s;\n", witIdent(kebab(s.short)))
	}
	b.WriteString("}\n")
	return []byte(b.String())
}

// uses are the types of the types interface a service's functions name,
// with what those name, in order, each once.
func (s *service) uses() []string {
	var out []string
	seen := map[string]bool{}
	var add func(m *message)
	add = func(m *message) {
		if len(m.fields) == 0 || seen[m.name] {
			return
		}
		seen[m.name] = true
		out = append(out, kebab(m.name))
	}
	for _, m := range s.methods {
		add(m.in)
		add(m.out)
	}
	return out
}

// witType is a field's WIT type.
func witType(f *field) string {
	if f.mapVal != nil {
		return "list<tuple<string, " + witType(f.mapVal) + ">>"
	}
	var t string
	switch {
	case f.msg != nil:
		t = witIdent(kebab(f.msg.name))
	case f.enum != nil:
		t = witIdent(kebab(f.enum.name))
	default:
		t = map[string]string{
			"string": "string", "bool": "bool", "bytes": "list<u8>",
			"int32": "s32", "sint32": "s32", "sfixed32": "s32",
			"uint32": "u32", "fixed32": "u32",
			"float": "f32", "double": "f64",
		}[f.scalar]
	}
	switch {
	case f.repeated:
		return "list<" + t + ">"
	case f.optional || f.msg != nil: // a message field may be absent
		return "option<" + t + ">"
	}
	return t
}

// enumCase is an enum value as a WIT case: LEVEL_INFO of Level is info.
func enumCase(enumName, value string) string {
	prefix := strings.ToUpper(strings.ReplaceAll(kebab(enumName), "-", "_")) + "_"
	return strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(value, prefix), "_", "-"))
}
