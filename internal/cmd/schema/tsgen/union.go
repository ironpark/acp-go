// Emission of tagged and raw unions.

package tsgen

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/ironpark/acp-go/internal/cmd/schema/tsdef"
)

// Doc lines of the JSON v2 methods generated types implement.
const (
	marshalDoc   = "// MarshalJSONTo implements [json.MarshalerTo].\n"
	unmarshalDoc = "// UnmarshalJSONFrom implements [json.UnmarshalerFrom].\n"
)

// memberKind is how a tagged-union member is represented in Go.
type memberKind int

const (
	memberLiteral memberKind = iota // object fixing the tag to a literal: a variant struct
	memberNested                    // union payload with a literal tag: a variant wrapping the payload
	memberCustom                    // object catch-all for other tags: a variant keeping every member
	memberOpen                      // catch-all that is not one object: decoded as the Unknown variant
	memberDefault                   // object without the tag: decoded when the tag is absent
)

// taggedMember is one alternative of a discriminated object union.
type taggedMember struct {
	kind   memberKind
	object *tsdef.Type // expanded object shape of literal, custom and default members
	nested string      // TypeScript name of a nested member's union payload
	base   string      // TypeScript name of the object a literal member extends: Base & { tag: "x" }
	ref    string      // TypeScript name of a default member that is a plain reference
	value  string      // quoted tag literal of literal and nested members
}

// tagIntersection recognizes `Ref & { tag: "literal" }`, returning the
// reference and the tag member.
func (g *generator) tagIntersection(m *tsdef.Type) (string, tsdef.Field, bool) {
	if m.Kind != tsdef.KindIntersection || len(m.Members) != 2 {
		return "", tsdef.Field{}, false
	}
	for i, part := range m.Members {
		other := m.Members[1-i]
		if part.Kind != tsdef.KindRef || other.Kind != tsdef.KindObject || len(other.Fields) != 1 || other.Element != nil {
			continue
		}
		f := other.Fields[0]
		if f.Optional || !stringLiteral(f.Type) {
			continue
		}
		return part.Name, f, true
	}
	return "", tsdef.Field{}, false
}

// stringLiteral reports whether t is a string literal type.
func stringLiteral(t *tsdef.Type) bool {
	k, _ := literals(t)
	return t.Kind == tsdef.KindLiteral && k == "string"
}

// taggedShape is a union member as tagged() sees it before a tag is chosen.
type taggedShape struct {
	expanded    *tsdef.Type // object or union expansion; nil for nested members
	base, ref   string      // see taggedMember
	nested      string
	nestedTag   string
	nestedValue string
}

// tagged recognizes unions discriminated by one string member: each known
// member fixes it to a distinct literal, at most one catch-all accepts any
// other value, and at most one member without the tag is the default.
func (g *generator) tagged(t *tsdef.Type) (tag string, members []taggedMember, ok bool, err error) {
	if len(t.Members) < 2 {
		return "", nil, false, nil
	}
	var shapes []taggedShape
	candidates := map[string]bool{}
	for _, m := range t.Members {
		base, tagField, intersection := g.tagIntersection(m)
		if intersection {
			// `Ref & { tag: "literal" }` where Ref expands to a union cannot be
			// flattened into one struct without losing its variants.
			if target, err := g.expand(&tsdef.Type{Kind: tsdef.KindRef, Name: base}, map[string]bool{}); err == nil && target.Kind == tsdef.KindUnion {
				shapes = append(shapes, taggedShape{nested: base, nestedTag: tagField.Name, nestedValue: tagField.Type.Literal})
				candidates[tagField.Name] = true
				continue
			}
		}
		expanded, err := g.expand(m, map[string]bool{})
		if err != nil {
			return "", nil, false, err
		}
		if expanded.Kind != tsdef.KindObject && expanded.Kind != tsdef.KindUnion {
			return "", nil, false, nil
		}
		shape := taggedShape{expanded: expanded}
		if intersection {
			shape.base = base
		}
		if m.Kind == tsdef.KindRef {
			shape.ref = m.Name
		}
		shapes = append(shapes, shape)
		if expanded.Kind == tsdef.KindObject {
			for _, f := range requiredLiterals(expanded) {
				if stringLiteral(f.Type) {
					candidates[f.Name] = true
				}
			}
		}
	}
	for _, tag := range slices.Sorted(maps.Keys(candidates)) {
		if members, ok := classify(tag, shapes); ok {
			return tag, members, true, nil
		}
	}
	return "", nil, false, nil
}

// classify maps each shape to a member kind for one candidate tag, reporting
// false when the shapes do not form a union discriminated by it.
func classify(tag string, shapes []taggedShape) ([]taggedMember, bool) {
	var members []taggedMember
	seen := map[string]bool{}
	catchAll, defaults := 0, 0
	for _, sh := range shapes {
		var m taggedMember
		switch {
		case sh.nested != "":
			if sh.nestedTag != tag {
				return nil, false
			}
			m = taggedMember{kind: memberNested, nested: sh.nested, value: sh.nestedValue}
		case sh.expanded.Kind == tsdef.KindUnion:
			if !catchAllObjects(sh.expanded, tag) {
				return nil, false
			}
			m = taggedMember{kind: memberOpen}
		default:
			field := fieldNamed(sh.expanded, tag)
			switch {
			case field == nil:
				m = taggedMember{kind: memberDefault, object: sh.expanded, ref: sh.ref}
			case field.Optional:
				return nil, false
			case stringLiteral(field.Type):
				m = taggedMember{kind: memberLiteral, object: sh.expanded, base: sh.base, value: field.Type.Literal}
			case isCatchAll(sh.expanded, tag):
				m = taggedMember{kind: memberCustom, object: sh.expanded}
			default:
				return nil, false
			}
		}
		switch m.kind {
		case memberLiteral, memberNested:
			if seen[m.value] {
				return nil, false
			}
			seen[m.value] = true
		case memberCustom, memberOpen:
			catchAll++
		case memberDefault:
			defaults++
		}
		members = append(members, m)
	}
	return members, len(seen) > 0 && catchAll <= 1 && defaults <= 1
}

// catchAllObjects reports whether every member of u is an object catch-all
// for tag: tag is a required string beside an index signature.
func catchAllObjects(u *tsdef.Type, tag string) bool {
	for _, m := range u.Members {
		if !isCatchAll(m, tag) {
			return false
		}
	}
	return true
}

// isCatchAll reports whether object t is a catch-all for tag: tag is a
// required string beside an index signature.
func isCatchAll(t *tsdef.Type, tag string) bool {
	f := fieldNamed(t, tag)
	return t.Kind == tsdef.KindObject && t.Element != nil && f != nil && !f.Optional && f.Type.Kind == tsdef.KindString
}

// fieldNamed returns the member of object t with the given JSON name, or nil.
func fieldNamed(t *tsdef.Type, name string) *tsdef.Field {
	for i := range t.Fields {
		if t.Fields[i].Name == name {
			return &t.Fields[i]
		}
	}
	return nil
}

// lowerFirst turns an exported name into an unexported one, lowering a
// leading initialism as a whole: MCPServer becomes mcpServer, not mCPServer.
func lowerFirst(s string) string {
	r := []rune(s)
	n := 0
	for n < len(r) && unicode.IsUpper(r[n]) {
		n++
	}
	if n > 1 && n < len(r) && unicode.IsLower(r[n]) {
		n-- // the last capital starts the next word: HTTPHeader -> httpHeader
	}
	for i := range max(n, 1) {
		r[i] = unicode.ToLower(r[i])
	}
	return string(r)
}

// openTags records a tagged union whose Zod rule must let unknown tags through
// to the Unknown variant.
type openTags struct {
	tag    string
	values []string
}

// taggedUnion emits a wrapper struct holding a sealed interface value. Each
// variant writes its own discriminator: literal members are structs whose tag
// is implied by the Go type, nested members wrap their union payload, the
// object catch-all keeps the tag as a field, and the default member has none.
// Unknown tags go to the catch-all, else to the default variant, else to a
// generated Unknown variant that keeps the JSON as received.
func (g *generator) taggedUnion(name, sdkDoc, tag string, members []taggedMember) error {
	iface := name + "Variant"
	constraint := name + "Variants"
	for _, n := range []string{iface, constraint, "New" + name} {
		if err := g.reserve(n); err != nil {
			return err
		}
	}
	marker := lowerFirst(name) + "Variant"
	unmarshalFn := "unmarshal" + iface
	if err := g.reserve(unmarshalFn); err != nil {
		return err
	}
	g.unmarshalers = append(g.unmarshalers, "json.UnmarshalFromFunc("+unmarshalFn+")")
	variantNames := make([]string, len(members))
	schemaNamed := make([]bool, len(members)) // the variant is a type the schema names and reserves
	absorbed := make([]*absorption, len(members))
	custom, open, defaultIndex := -1, false, -1
	known := openTags{tag: tag}
	for i, m := range members {
		switch m.kind {
		case memberCustom:
			custom = i
			variantNames[i] = name + "Custom"
		case memberOpen:
			open = true
			continue
		case memberDefault:
			defaultIndex = i
			// The schema type itself can be the default variant when nothing
			// else refers to it: the variant methods leave its JSON unchanged,
			// since the default variant has no tag.
			if _, ok := g.soleStruct(m.ref); ok {
				variantNames[i], schemaNamed[i] = Name(m.ref), true
				continue
			}
			variantNames[i] = name + "Untagged"
		case memberLiteral, memberNested:
			value, err := strconv.Unquote(m.value)
			if err != nil {
				return err
			}
			known.values = append(known.values, value)
			variantNames[i] = name + Name(value)
			if absorbed[i] = g.absorbedBy(m.base, name, m.value); absorbed[i] != nil && Name(m.base) == variantNames[i] {
				schemaNamed[i] = true
				continue
			}
		}
		if g.names[variantNames[i]] {
			// An unrelated schema type already has the name.
			variantNames[i] += "Variant"
		}
	}
	unknown := ""
	if custom < 0 && (open || defaultIndex < 0) {
		unknown = name + "Unknown"
		if g.names[unknown] {
			unknown += "Variant"
		}
		if !open {
			// The SDK rejects unknown tags here; let them through to Unknown.
			g.openTags[name] = known
		}
	}
	fallback := unknown
	switch {
	case custom >= 0:
		fallback = variantNames[custom]
	case unknown == "":
		fallback = variantNames[defaultIndex]
	}
	var variants, implementers []string
	for i, v := range variantNames {
		if members[i].kind != memberOpen {
			variants = append(variants, v)
		}
	}
	if unknown != "" {
		variants = append(variants, unknown)
	}
	for _, v := range variants {
		implementers = append(implementers, "["+v+"]")
	}
	g.decls[name] = Decl{Interface: iface, Constraint: constraint, Constructor: "New" + name, Variants: variants}
	g.write("%s", doc(name, sdkDoc, fmt.Sprintf("%s is a tagged union discriminated by the %q member. Use [New%s]\nor a type switch on [%s.Variant] to work with it. The zero value holds no\nvariant: an omitzero field omits it, and encoding it anywhere else fails.", name, tag, name, name)))
	g.write("type %s struct{ value %s }\n", name, iface)
	g.write("// %s is implemented by %s.\n", iface, strings.Join(implementers, ", "))
	g.write("type %s interface { %s(); Tag() string }\n", iface, marker)
	g.write("// %s is the set of %s variant types. It lists the types\n// themselves: a pointer to a variant also has the %s\n// methods, but is not one.\n", constraint, name, iface)
	g.write("type %s interface {\n%s\n%s\n}\n", constraint, strings.Join(variants, " |\n"), iface)
	g.write("// New%s wraps a variant.\n", name)
	g.write("func New%s[T %s](v T) %s { return %s{value: v} }\n", name, constraint, name, name)
	g.write("// Variant returns the wrapped variant, or nil for the zero value.\n")
	g.write("func (u %s) Variant() %s { return u.value }\n", name, iface)
	g.write("// As returns the variant if it is a T, like a type assertion on Variant\n// with T checked against the union's variants at compile time.\n")
	g.write("func (u %s) As[T %s]() (T, bool) { v, ok := u.value.(T); return v, ok }\n", name, constraint)
	g.write("// Tag returns the %q discriminator, or \"\" for the zero value.\n", tag)
	g.write("func (u %s) Tag() string { if u.value == nil { return \"\" }; return u.value.Tag() }\n", name)
	g.write("// IsZero reports whether no variant is set, so omitzero omits the field.\n")
	g.write("func (u %s) IsZero() bool { return u.value == nil }\n", name)
	g.write("%s", marshalDoc)
	g.write("func (u %s) MarshalJSONTo(enc *jsontext.Encoder) error { if u.value == nil { return errors.New(\"%s: no variant set; use New%s\") }; return json.MarshalEncode(enc, u.value) }\n", name, name, name)
	g.write("%s", unmarshalDoc)
	g.write("func (u *%s) UnmarshalJSONFrom(dec *jsontext.Decoder) error { return %s(dec, &u.value) }\n", name, unmarshalFn)
	g.write("// %s decodes a %s by its %q member; it backs Unmarshalers.\n", unmarshalFn, iface, tag)
	g.write("func %s(dec *jsontext.Decoder, out *%s) error {\n", unmarshalFn, iface)
	g.write("raw, err := dec.ReadValue(); if err != nil { return err }\n")
	g.write("if raw.Kind() == 'n' { *out = nil; return nil }\n")
	g.write("if raw.Kind() != '{' { return fmt.Errorf(\"%s: expected object, got %%s\", raw.Kind()) }\n", name)
	decode := func(variant string) string {
		return fmt.Sprintf("var v %s; if err := json.Unmarshal(raw, &v, dec.Options()); err != nil { return err }; *out = v\n", variant)
	}
	present := "_"
	if defaultIndex >= 0 {
		// A missing tag selects the default variant, so absent is told from empty.
		present = "present"
	}
	g.write("tag, %s, err := union.ReadTag(raw, %q, dec.Options()); if err != nil { return fmt.Errorf(\"%s: %%w\", err) }\n", present, tag, name)
	if defaultIndex >= 0 {
		g.write("if !present { %sreturn nil }\n", decode(variantNames[defaultIndex]))
	}
	g.write("switch tag {\n")
	for i, m := range members {
		if m.kind == memberLiteral || m.kind == memberNested {
			g.write("case %s: %s", m.value, decode(variantNames[i]))
		}
	}
	if fallback == unknown {
		g.write("default: *out = %s{Raw: raw.Clone()}\n", unknown)
	} else {
		// As in the SDK, an unrecognized tag does not stop a payload that
		// fits the catch-all or default variant.
		g.write("default: %s", decode(fallback))
	}
	g.write("}\nreturn nil\n}\n")

	if unknown != "" {
		if err := g.reserve(unknown); err != nil {
			return err
		}
		g.write("// %s holds %s values whose %q this SDK does not know. Raw is the\n// object as received and is encoded unchanged.\n", unknown, name, tag)
		g.write("type %s struct { Raw jsontext.Value }\n", unknown)
		g.write("func (%s) %s() {}\n", unknown, marker)
		g.write("// Tag returns the %q member of Raw.\n", tag)
		g.write("func (v %s) Tag() string { tag, _, _ := union.ReadTag(v.Raw, %q); return tag }\n", unknown, tag)
		g.write("%s", marshalDoc)
		g.write("func (v %s) MarshalJSONTo(enc *jsontext.Encoder) error { return enc.WriteValue(v.Raw) }\n", unknown)
	}

	for i, m := range members {
		if m.kind == memberOpen {
			continue
		}
		vname := variantNames[i]
		if !schemaNamed[i] {
			if err := g.reserve(vname); err != nil {
				return err
			}
		}
		if m.object != nil {
			skip := ""
			if m.kind == memberLiteral {
				skip = tag
			}
			if err := tagClash(vname, m.object, skip); err != nil {
				return err
			}
		}
		switch m.kind {
		case memberCustom:
			g.write("// %s holds %s values with an unrecognized %q, keeping every member.\n", vname, name, tag)
			if err := g.structType(vname, m.object, ""); err != nil {
				return fmt.Errorf("%s: %w", vname, err)
			}
			g.write("func (%s) %s() {}\n", vname, marker)
			g.write("// Tag returns the %q member.\n", tag)
			g.write("func (v %s) Tag() string { return v.%s }\n", vname, Name(tag))
		case memberDefault:
			if schemaNamed[i] {
				// The schema's own type is the variant; it is declared with the
				// other object types and gains the variant methods here.
				g.write("\n")
			} else {
				g.write("// %s is the %s variant without a %q member.\n", vname, name, tag)
				if err := g.structType(vname, m.object, ""); err != nil {
					return fmt.Errorf("%s: %w", vname, err)
				}
			}
			g.write("func (%s) %s() {}\n", vname, marker)
			g.write("// Tag returns \"\": %s is the %s variant without a %q member.\n", vname, name, tag)
			g.write("func (%s) Tag() string { return \"\" }\n", vname)
		case memberNested:
			payload := Name(m.nested)
			g.write("// %s is the %s variant with %s %s; its payload is the %s union\n// whose members are written alongside the discriminator.\n", vname, name, tag, m.value, payload)
			g.write("type %s struct { Value %s }\n", vname, payload)
			g.write("func (%s) %s() {}\n", vname, marker)
			g.write("// Tag returns %s.\n", m.value)
			g.write("func (%s) Tag() string { return %s }\n", vname, m.value)
			g.write("%s", marshalDoc)
			g.write("func (v %s) MarshalJSONTo(enc *jsontext.Encoder) error { return union.SpliceTag(enc, %q, %s, v.Value) }\n", vname, tag, m.value)
			g.write("%s", unmarshalDoc)
			g.write("func (v *%s) UnmarshalJSONFrom(dec *jsontext.Decoder) error { return union.UnspliceTag(dec, %q, %s, &v.Value) }\n", vname, tag, m.value)
		case memberLiteral:
			variantDoc := fmt.Sprintf("%s is the %s variant with %s %s.", vname, name, tag, m.value)
			if a := absorbed[i]; a != nil {
				// The variant took over the schema type it extends: it keeps
				// that type's comment and inherits its Zod rule, with the tag.
				g.write("%s", doc(vname, g.docs[m.base], variantDoc))
				a.variant = vname
			} else {
				g.write("// %s\n", variantDoc)
			}
			if err := g.structType(vname, m.object, tag); err != nil {
				return fmt.Errorf("%s: %w", vname, err)
			}
			fields := lowerFirst(vname) + "Fields"
			wire := lowerFirst(vname) + "Wire"
			g.write("func (%s) %s() {}\n", vname, marker)
			g.write("// Tag returns %s.\n", m.value)
			g.write("func (%s) Tag() string { return %s }\n", vname, m.value)
			g.write("type %s %s\n", fields, vname)
			g.write("type %s struct { Tag string `json:%q`; %s `json:\",embed\"` }\n", wire, tag, fields)
			g.write("%s", marshalDoc)
			g.write("func (v %s) MarshalJSONTo(enc *jsontext.Encoder) error { return json.MarshalEncode(enc, %s{%s, %s(v)}) }\n", vname, wire, m.value, fields)
			g.write("%s", unmarshalDoc)
			g.write("func (v *%s) UnmarshalJSONFrom(dec *jsontext.Decoder) error {\n", vname)
			g.write("var w %s; if err := json.UnmarshalDecode(dec, &w); err != nil { return err }\n", wire)
			mismatch := strconv.Quote(fmt.Sprintf("%s: expected %s %s, got %%q", vname, tag, m.value))
			g.write("if w.Tag != %s { return fmt.Errorf(%s, w.Tag) }\n", m.value, mismatch)
			g.write("*v = %s(w.%s); return nil\n}\n", vname, fields)
		}
	}
	return nil
}

// soleStruct reports whether the schema type ref is an object struct that only
// one reference in the schema uses, returning its expansion. A tagged union
// can take such a type over as one of its variants.
func (g *generator) soleStruct(ref string) (*tsdef.Type, bool) {
	if g.refs[ref] != 1 {
		return nil, false
	}
	f, t, err := g.form(g.defs[ref])
	return t, err == nil && f == formStruct
}

// tagClash reports a variant struct that would declare a field named Tag
// beside its Tag method.
func tagClash(variant string, object *tsdef.Type, skip string) error {
	for _, f := range object.Fields {
		if f.Name != skip && Name(f.Name) == "Tag" {
			return fmt.Errorf("%s: field %s collides with the variant's Tag method", variant, f.Name)
		}
	}
	return nil
}

// absorption records a schema type that a tagged union took over as the
// variant for one of its tags.
type absorption struct {
	base    string // TypeScript name of the absorbed type
	union   string // Go name of the union
	tag     string // the union's tag member
	value   string // quoted tag literal
	variant string // Go name of the variant, set when the union is emitted
}

// absorbedBy returns the absorption of base by union's variant for value, or
// nil when there is none.
func (g *generator) absorbedBy(base, union, value string) *absorption {
	if a := g.absorbed[Name(base)]; a != nil && a.base == base && a.union == union && a.value == value {
		return a
	}
	return nil
}

// planVariants finds, before anything is emitted, the schema types that
// tagged unions take over as variants: Base in `Base & { tag: "x" }` when
// nothing else refers to Base. The variant keeps its usual name, which is often
// Base's own (MCPServer + "http" = MCPServerHTTP); either way Base is not
// declared a second time with the same members.
func (g *generator) planVariants() error {
	for _, d := range g.pending {
		f, t, err := g.form(d.Type)
		if err != nil || f != formUnion {
			continue
		}
		tag, members, ok, err := g.tagged(t)
		if err != nil {
			return fmt.Errorf("%s: %w", d.Name, err)
		}
		if !ok {
			continue
		}
		for _, m := range members {
			if m.kind != memberLiteral || m.base == "" {
				continue
			}
			if bt, ok := g.soleStruct(m.base); !ok || len(requiredLiterals(bt)) > 0 {
				continue
			}
			g.absorbed[Name(m.base)] = &absorption{base: m.base, union: d.Name, tag: tag, value: m.value}
		}
	}
	return nil
}

// memberLabel names a union alternative for generated identifiers. Objects
// are named by their literal members; an object without any is "Custom" when
// it is a catch-all with an index signature and otherwise unnamed (""), left
// for [labelUnions] to name by its members.
func (g *generator) memberLabel(m, expanded *tsdef.Type) (string, error) {
	switch m.Kind {
	case tsdef.KindRef:
		return Name(m.Name), nil
	case tsdef.KindLiteral:
		if stringLiteral(m) {
			value, err := strconv.Unquote(m.Literal)
			if err != nil {
				return "", err
			}
			return Name(value), nil
		}
		return Name(m.Literal), nil
	case tsdef.KindNull:
		return "Null", nil
	case tsdef.KindString:
		return "String", nil
	case tsdef.KindNumber:
		return "Number", nil
	case tsdef.KindBoolean:
		return "Bool", nil
	case tsdef.KindUnknown, tsdef.KindAny:
		return "Unknown", nil
	case tsdef.KindIntersection:
		var label strings.Builder
		for _, part := range m.Members {
			if part.Kind == tsdef.KindObject {
				label.WriteString(literalLabel(part))
			}
		}
		if label.String() != "" {
			return label.String(), nil
		}
	case tsdef.KindArray:
		element, err := g.memberLabel(m.Element, m.Element)
		if err != nil {
			return "", err
		}
		return element + "List", nil
	}
	if expanded.Kind == tsdef.KindObject {
		if label := literalLabel(expanded); label != "" {
			return label, nil
		}
		if expanded.Element != nil {
			return "Custom", nil
		}
		return "", nil
	}
	return "Variant", nil
}

// labelUnions completes the labels of union members: an unnamed object is
// named by the required members no other member declares, and members that
// share a label get the members that set them apart within that group
// appended (Form, Form -> FormSession, FormRequest). Only if that is not
// enough does a position number break the tie.
func labelUnions(labels []string, expanded []*tsdef.Type) {
	all := make([]int, len(labels))
	for i := range all {
		all[i] = i
	}
	for i, label := range labels {
		if label == "" {
			if labels[i] = fieldsLabel(i, all, expanded); labels[i] == "" {
				labels[i] = "Object"
			}
		}
	}
	groups := map[string][]int{}
	for i, label := range labels {
		groups[label] = append(groups[label], i)
	}
	for label, group := range groups {
		if len(group) > 1 {
			for _, i := range group {
				labels[i] = label + fieldsLabel(i, group, expanded)
			}
		}
	}
	used := map[string]bool{}
	for i := range labels {
		// The numbered label can itself be taken, by an earlier member
		// labelled Form3 for instance; count on until one is free.
		if base := labels[i]; used[base] {
			for n := i + 1; used[labels[i]]; n++ {
				labels[i] = base + strconv.Itoa(n)
			}
		}
		used[labels[i]] = true
	}
}

// fieldsLabel names member i by its required members that no other member of
// group declares. An id member names what it identifies: sessionId gives
// Session, since a type named …SessionID reads as an identifier.
func fieldsLabel(i int, group []int, expanded []*tsdef.Type) string {
	if expanded[i].Kind != tsdef.KindObject {
		return ""
	}
	var label strings.Builder
	for _, f := range expanded[i].Fields {
		if f.Optional || declaredByOther(f.Name, i, group, expanded) {
			continue
		}
		word := Name(f.Name)
		if trimmed := strings.TrimSuffix(word, "ID"); trimmed != "" {
			word = trimmed
		}
		label.WriteString(word)
	}
	return label.String()
}

// declaredByOther reports whether a member of group other than i declares field.
func declaredByOther(field string, i int, group []int, expanded []*tsdef.Type) bool {
	for _, j := range group {
		if j == i || expanded[j].Kind != tsdef.KindObject {
			continue
		}
		if fieldNamed(expanded[j], field) != nil {
			return true
		}
	}
	return false
}

func (g *generator) union(name, sdkDoc string, t *tsdef.Type) error {
	tag, members, ok, err := g.tagged(t)
	if err != nil {
		return err
	}
	if ok {
		return g.taggedUnion(name, sdkDoc, tag, members)
	}
	constraint := name + "Alternative"
	table := lowerFirst(name) + "Alternatives"
	for _, n := range []string{"New" + name, constraint, table} {
		if err := g.reserve(n); err != nil {
			return err
		}
	}
	g.decls[name] = Decl{Constraint: constraint, Constructor: "New" + name}
	// One rule per alternative, grouped by the canonical Go type so aliases of
	// the same type become one type-set term.
	var terms []string
	rules := map[string][]string{}
	expanded := make([]*tsdef.Type, len(t.Members))
	labels := make([]string, len(t.Members))
	for i, m := range t.Members {
		var err error
		if expanded[i], err = g.expand(m, map[string]bool{}); err != nil {
			return err
		}
		if labels[i], err = g.memberLabel(m, expanded[i]); err != nil {
			return err
		}
	}
	labelUnions(labels, expanded)
	for i, m := range t.Members {
		label := labels[i]
		expanded := expanded[i]
		expr, err := g.expr(m, name+label)
		if err != nil {
			return err
		}
		key := g.canonical(m, expr)
		if _, ok := rules[key]; !ok {
			terms = append(terms, key)
		}
		if rule := g.altRule(expanded); !slices.Contains(rules[key], rule) {
			rules[key] = append(rules[key], rule)
		}
	}
	// The zero value holds no payload. It encodes as null where null is one
	// of the alternatives, and otherwise fails to encode.
	empty := fmt.Sprintf("errors.New(%q)", name+": no value set; use New"+name)
	zeroDoc := "The zero value holds no payload: an omitzero field omits it, and encoding it\nanywhere else fails."
	if g.acceptsNull(t, map[string]bool{}) {
		empty = "enc.WriteToken(jsontext.Null)"
		zeroDoc = "The zero value holds no payload and encodes as null."
	}
	g.write("%s", doc(name, sdkDoc, fmt.Sprintf("%s preserves the complete JSON payload, including future variants.\nUse [%s.As] to read one alternative and [New%s] to build one.\n%s", name, name, name, zeroDoc)))
	g.write("type %s struct { raw jsontext.Value }\n", name)
	g.write("// %s is the set of Go types %s can hold.\n", constraint, name)
	g.write("type %s interface { %s }\n", constraint, strings.Join(terms, " | "))
	g.write("var %s = union.Table(\n", table)
	for _, key := range terms {
		g.write("union.Alt[%s](%s),\n", key, strings.Join(rules[key], ", "))
	}
	g.write(")\n")
	g.write("// New%s encodes value, one of the %s types, adding any literal members\n// it requires and rejecting values that are not that alternative.\n", name, constraint)
	g.write("func New%s[T %s](value T) (%s, error) { raw, err := union.New(%q, %s, value); return %s{raw: raw}, err }\n", name, constraint, name, name, table, name)
	g.write("// As decodes the payload as the alternative T, or reports why it is not one.\n")
	g.write("func (v %s) As[T %s]() (T, error) { return union.As[T](%q, %s, v.raw) }\n", name, constraint, name, table)
	g.write("// RawJSON returns a copy of the payload as received or built.\n")
	g.write("func (v %s) RawJSON() jsontext.Value {return v.raw.Clone()}\n", name)
	g.write("// IsZero reports whether no payload is stored, so omitzero omits the field.\n")
	g.write("func (v %s) IsZero() bool {return len(v.raw)==0}\n", name)
	g.write("%s", marshalDoc)
	g.write("func (v %s) MarshalJSONTo(enc *jsontext.Encoder) error {if len(v.raw)==0{return %s};return enc.WriteValue(v.raw)}\n", name, empty)
	g.write("%s", unmarshalDoc)
	g.write("func (v *%s) UnmarshalJSONFrom(dec *jsontext.Decoder) error {raw,err:=dec.ReadValue();if err!=nil{return err};v.raw=raw.Clone();return nil}\n", name)
	return nil
}

// altRule renders the union.Rule literal that recognizes one union alternative.
func (g *generator) altRule(expanded *tsdef.Type) string {
	var parts []string
	if expanded.Kind == tsdef.KindNull {
		parts = append(parts, "Null: true")
	}
	if !g.acceptsNull(expanded, map[string]bool{}) {
		parts = append(parts, "NonNull: true")
	}
	if expanded.Kind == tsdef.KindLiteral {
		parts = append(parts, fmt.Sprintf("Literal: jsontext.Value(%q)", expanded.Literal))
	}
	if expanded.Kind == tsdef.KindObject {
		var required, notNull, tags []string
		for _, f := range expanded.Fields {
			if !f.Optional {
				required = append(required, strconv.Quote(f.Name))
			}
			if !g.acceptsNull(f.Type, map[string]bool{}) {
				notNull = append(notNull, strconv.Quote(f.Name))
			}
		}
		for _, f := range requiredLiterals(expanded) {
			tags = append(tags, fmt.Sprintf("{Name: %q, Value: jsontext.Value(%q)}", f.Name, f.Type.Literal))
		}
		if len(required) > 0 {
			parts = append(parts, "Required: []string{"+strings.Join(required, ", ")+"}")
		}
		if len(notNull) > 0 {
			parts = append(parts, "NotNull: []string{"+strings.Join(notNull, ", ")+"}")
		}
		if len(tags) > 0 {
			slices.Sort(tags)
			parts = append(parts, "Tags: []union.Tag{"+strings.Join(tags, ", ")+"}")
		}
	}
	return "union.Rule{" + strings.Join(parts, ", ") + "}"
}

// isAbsorbed reports whether the schema type with the given Go name was taken
// over by a tagged union, which declares it.
func (g *generator) isAbsorbed(goName string) bool {
	return g.absorbed[goName] != nil
}
