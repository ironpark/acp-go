// Emission of top-level definitions: aliases, enums and intersection expansion.

package tsgen

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/ironpark/acp-go/internal/cmd/schema/tsdef"
)

func (g *generator) add(name string, t *tsdef.Type) string {
	base := name
	for i := 2; g.names[name]; i++ {
		name = fmt.Sprintf("%s%d", base, i)
	}
	g.names[name] = true
	g.pending = append(g.pending, tsdef.Definition{Name: name, Type: t})
	return name
}

// form is how definition() emits a top-level definition.
type form int

const (
	formEnum     form = iota // literal union: named scalar with constants
	formOpenEnum             // literal union that also admits its base type
	formNamed                // distinct scalar type, e.g. SessionID string
	formStruct               // object with fields
	formUnion                // union wrapper
	formAlias                // type X = expr
)

// formed is a memoized result of [generator.form].
type formed struct {
	form form
	t    *tsdef.Type
	err  error
}

// form classifies t, expanding intersections first. It is the single source
// for both emission and canonical's alias following, and it is asked about
// the same types repeatedly, so results are kept per type.
func (g *generator) form(t *tsdef.Type) (form, *tsdef.Type, error) {
	if r, ok := g.forms[t]; ok {
		return r.form, r.t, r.err
	}
	f, expanded, err := g.classifyForm(t)
	g.forms[t] = formed{f, expanded, err}
	return f, expanded, err
}

func (g *generator) classifyForm(t *tsdef.Type) (form, *tsdef.Type, error) {
	if _, ok := literals(t); ok {
		return formEnum, t, nil
	}
	if _, _, ok := openEnum(t); ok {
		return formOpenEnum, t, nil
	}
	if t.Kind == tsdef.KindIntersection {
		expanded, err := g.expand(t, map[string]bool{})
		if err != nil {
			return 0, nil, err
		}
		t = expanded
	}
	switch t.Kind {
	case tsdef.KindObject:
		if len(t.Fields) == 0 {
			return formAlias, t, nil // records alias map[string]T
		}
		return formStruct, t, nil
	case tsdef.KindUnion:
		if nonnull, isNull := t.NonNull(); isNull && nonnull.Kind != tsdef.KindUnion {
			return formAlias, t, nil
		}
		return formUnion, t, nil
	case tsdef.KindString, tsdef.KindNumber, tsdef.KindBoolean:
		return formNamed, t, nil
	}
	return formAlias, t, nil
}

// aliasDefinition reports whether definition() emits d as "type X = ..."
// rather than as a distinct named type.
func (g *generator) aliasDefinition(d *tsdef.Type) bool {
	f, _, err := g.form(d)
	return err == nil && f == formAlias
}

func (g *generator) definition(d tsdef.Definition) error {
	if g.isAbsorbed(d.Name) {
		return nil // declared by the tagged union that took it over as a variant
	}
	f, t, err := g.form(d.Type)
	if err != nil {
		return err
	}
	file := fileTypes
	switch {
	case f == formEnum || f == formOpenEnum || f == formNamed:
		file = fileEnums
	case Envelope(d.Name):
		file = fileEnvelope
	case f == formUnion:
		file = fileUnions
	}
	g.use(file)
	g.write("\n")
	switch f {
	case formEnum:
		kind, _ := literals(t)
		members := t.Members
		if t.Kind == tsdef.KindLiteral {
			members = []*tsdef.Type{t}
		}
		return g.enum(d.Name, d.Comment, kind, members, false)
	case formOpenEnum:
		base, members, _ := openEnum(t)
		kind, _ := literals(members[0])
		if base.Kind == tsdef.KindNumber && base.Number != "" {
			kind = base.Number
		}
		return g.enum(d.Name, d.Comment, kind, members, true)
	case formStruct:
		g.write("%s", doc(d.Name, d.Comment))
		return g.structType(d.Name, t, "")
	case formUnion:
		return g.union(d.Name, d.Comment, t)
	}
	g.write("%s", doc(d.Name, d.Comment))
	expr, err := g.expr(t, d.Name+"Value")
	if err != nil {
		return err
	}
	if f == formNamed {
		// Distinct identifier types (SessionID vs ToolCallID) cannot be mixed
		// up, and they can carry their own Zod unmarshaler.
		g.write("type %s %s\n", d.Name, expr)
		return nil
	}
	g.alias(d.Name, expr)
	return nil
}

// alias emits "type name = expr" and records it so Zod rules skip the alias:
// it shares a reflect.Type with expr.
func (g *generator) alias(name, expr string) {
	g.aliases[name] = true
	g.write("type %s = %s\n", name, expr)
}

// enum emits a documented named scalar type with one constant per literal. Open enums
// also accept values outside the listed constants.
func (g *generator) enum(typeName, sdkDoc, kind string, members []*tsdef.Type, open bool) error {
	var note string
	if open {
		note = typeName + " also accepts values outside the listed constants; use [" + typeName + ".Known] to check."
	}
	g.write("%stype %s %s\n", doc(typeName, sdkDoc, note), typeName, kind)
	var names []string
	g.write("const (\n")
	for _, m := range members {
		value := m.Literal
		label := value
		if kind == "string" {
			var err error
			label, err = strconv.Unquote(value)
			if err != nil {
				return err
			}
		}
		name := typeName + Name(label)
		if override, ok := literalNames[typeName][value]; ok {
			name = typeName + override // used as written
		}
		if g.names[name] {
			return fmt.Errorf("enum constant collision %s", name)
		}
		g.names[name] = true
		names = append(names, name)
		g.write("%s %s = %s\n", name, typeName, value)
	}
	g.write(")\n")
	g.decls[typeName] = Decl{Constants: names}
	if open {
		g.write("// Known reports whether v is one of the protocol-defined constants.\n")
		g.write("func (v %s) Known() bool {\nswitch v {\ncase %s:\nreturn true\n}\nreturn false\n}\n", typeName, strings.Join(names, ", "))
	}
	return nil
}

// expand distributes intersections over unions and merges object members.
func (g *generator) expand(t *tsdef.Type, seen map[string]bool) (*tsdef.Type, error) {
	if t.Kind == tsdef.KindRef {
		if seen[t.Name] {
			return nil, fmt.Errorf("cyclic intersection through %s", t.Name)
		}
		target, ok := g.defs[t.Name]
		if !ok {
			return nil, fmt.Errorf("unresolved type %s", t.Name)
		}
		seen[t.Name] = true
		out, err := g.expand(target, seen)
		delete(seen, t.Name)
		return out, err
	}
	if t.Kind == tsdef.KindUnion {
		var members []*tsdef.Type
		for _, member := range t.Members {
			expanded, err := g.expand(member, seen)
			if err != nil {
				return nil, err
			}
			if expanded.Kind == tsdef.KindUnion {
				members = append(members, expanded.Members...)
			} else {
				members = append(members, expanded)
			}
		}
		return &tsdef.Type{Kind: tsdef.KindUnion, Members: members}, nil
	}
	if t.Kind != tsdef.KindIntersection {
		return t, nil
	}
	variants := []*tsdef.Type{{Kind: tsdef.KindObject}}
	for _, member := range t.Members {
		m, err := g.expand(member, seen)
		if err != nil {
			return nil, err
		}
		alternatives := []*tsdef.Type{m}
		if m.Kind == tsdef.KindUnion {
			alternatives = m.Members
		}
		var next []*tsdef.Type
		for _, base := range variants {
			for _, alt := range alternatives {
				a, err := g.expand(alt, seen)
				if err != nil {
					return nil, err
				}
				if a.Kind != tsdef.KindObject {
					return nil, fmt.Errorf("intersection member is %s, expected object", a.Kind)
				}
				merged := &tsdef.Type{Kind: tsdef.KindObject, Fields: append([]tsdef.Field(nil), base.Fields...), Element: base.Element}
				if a.Element != nil {
					if merged.Element != nil && !reflect.DeepEqual(merged.Element, a.Element) {
						return nil, fmt.Errorf("intersection merges conflicting index signatures")
					}
					merged.Element = a.Element
				}
				for _, f := range a.Fields {
					found := false
					for i, old := range merged.Fields {
						if old.Name == f.Name {
							found = true
							f.Optional = f.Optional && old.Optional
							// A literal narrows a string: the literal is kept.
							switch {
							case reflect.DeepEqual(f.Type, old.Type):
							case f.Type.Kind == tsdef.KindLiteral && old.Type.Kind == tsdef.KindString:
							case old.Type.Kind == tsdef.KindLiteral && f.Type.Kind == tsdef.KindString:
								f.Type = old.Type
							default:
								return nil, fmt.Errorf("unsupported intersection for property %s", f.Name)
							}
							merged.Fields[i] = f
							break
						}
					}
					if !found {
						merged.Fields = append(merged.Fields, f)
					}
				}
				next = append(next, merged)
			}
		}
		variants = next
	}
	if len(variants) == 1 {
		return variants[0], nil
	}
	return &tsdef.Type{Kind: tsdef.KindUnion, Members: variants}, nil
}

func (g *generator) reserve(name string) error {
	if g.names[name] {
		return fmt.Errorf("duplicate Go declaration %s", name)
	}
	g.names[name] = true
	return nil
}
