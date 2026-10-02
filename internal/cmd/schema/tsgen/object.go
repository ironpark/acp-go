// Emission of object types as Go structs.

package tsgen

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ironpark/acp-go/internal/cmd/schema/tsdef"
)

// structType emits a struct declaration, omitting the JSON member named skip
// (used for union discriminators that are implied by the Go type).
func (g *generator) structType(name string, t *tsdef.Type, skip string) error {
	if len(t.Fields) == 0 {
		expr, err := g.expr(t, name+"Value")
		if err != nil {
			return err
		}
		g.alias(name, expr)
		return nil
	}
	g.write("type %s struct {\n", name)
	payload := !Envelope(name)
	names := map[string]bool{}
	var pointers []getter
	for _, f := range t.Fields {
		if f.Name == skip {
			continue
		}
		field := Name(f.Name)
		if field == "AdditionalProperties" && t.Element != nil {
			return fmt.Errorf("reserved field %s", field)
		}
		if names[field] {
			return fmt.Errorf("field collision %s", field)
		}
		names[field] = true
		typ := f.Type
		if f.Tristate {
			typ, _ = typ.NonNull()
		}
		expr, err := g.expr(typ, name+field)
		if err != nil {
			return err
		}
		tag := f.Name
		text := fieldDoc(f.Comment)
		// The extensibility spec reserves _meta on every message; one shared
		// type gives it Set/Get helpers in every version.
		meta := func() {
			if f.Name == "_meta" && expr == "map[string]jsontext.Value" {
				expr, g.usesMeta = "Meta", true
				text = metaDoc(f.Comment)
			}
		}
		if f.Tristate {
			meta()
			// Absent and null mean different things here, which no Go
			// zero value can carry for both.
			g.write("%s%s optional.Value[%s] `json:%q`\n", comment(text), field, expr, tag+",omitzero")
			if payload {
				pointers = append(pointers, getter{typ: name, field: field, elem: expr, object: g.isStruct(typ), tristate: true})
			}
			continue
		}
		if f.Optional {
			// omitzero already distinguishes nil collections from empty ones, so
			// optional slices and maps do not need a pointer. Optional null and
			// absence share the nil representation.
			if collection(strings.TrimPrefix(expr, "*")) || g.isUnion(f.Type) {
				expr = strings.TrimPrefix(expr, "*")
			} else if !strings.HasPrefix(expr, "*") && expr != "jsontext.Value" {
				expr = "*" + expr
			}
			tag += ",omitzero"
		}
		if !f.Optional && f.Type.Kind == tsdef.KindLiteral {
			text = strings.TrimSpace(text + "\n\nAlways " + f.Type.Literal + ": MarshalJSONTo writes it whatever the field holds.")
		}
		meta()
		g.write("%s%s %s `json:%q`\n", comment(text), field, expr, tag)
		if payload && strings.HasPrefix(expr, "*") {
			pointers = append(pointers, getter{typ: name, field: field, elem: strings.TrimPrefix(expr, "*"), object: g.isStruct(f.Type)})
		}
	}
	for _, p := range pointers {
		if names["Get"+p.field] {
			return fmt.Errorf("getter Get%s collides with a field", p.field)
		}
	}
	g.getters = append(g.getters, pointers...)
	if t.Element != nil {
		element, err := g.expr(t.Element, name+"AdditionalProperty")
		if err != nil {
			return err
		}
		g.write("AdditionalProperties map[string]%s `json:\",embed\"`\n", element)
	}
	g.write("}\n")
	var fixed []string
	for _, f := range requiredLiterals(t) {
		if f.Name != skip {
			fixed = append(fixed, fmt.Sprintf("v.%s = %s; ", Name(f.Name), f.Type.Literal))
		}
	}
	if len(fixed) > 0 {
		// The literal members are part of the wire shape, so a zero value still
		// encodes as this type and raw-union constructors need no splicing.
		set := strings.Join(fixed, "")
		g.write("// MarshalJSONTo encodes v with its literal members fixed.\n")
		g.write("func (v %s) MarshalJSONTo(enc *jsontext.Encoder) error { %stype plain %s; return json.MarshalEncode(enc, plain(v)) }\n", name, set, name)
	}
	return nil
}

// getter is a pointer or tristate field that gets a nil-safe accessor.
type getter struct {
	typ, field, elem string // struct, field and pointed-to (or held) type
	object           bool   // elem is a struct, returned as a pointer
	tristate         bool   // the field is an optional.Value of elem
}

// emitGetters writes a GetX method for every pointer field of a payload
// struct, protobuf style. A pointer to a struct is returned as is, so calls
// chain through absent objects; any other pointer is dereferenced, giving the
// zero value when it is nil. The receiver may be nil too.
func (g *generator) emitGetters() {
	if len(g.getters) == 0 {
		return
	}
	g.use(fileGetters)
	// Group by type; within a type, fields keep their declaration order.
	slices.SortStableFunc(g.getters, func(a, b getter) int { return strings.Compare(a.typ, b.typ) })
	for _, p := range g.getters {
		if p.tristate && p.object {
			g.write("// Get%[2]s returns a copy of %[2]s, or nil if x is nil or %[2]s is absent or null.\n", p.typ, p.field)
			g.write("func (x *%s) Get%s() *%s {\nif x != nil {\nif v, ok := x.%s.Get(); ok {\nreturn &v\n}\n}\nreturn nil\n}\n\n", p.typ, p.field, p.elem, p.field)
			continue
		}
		if p.tristate {
			g.write("// Get%[2]s returns the value of %[2]s, or the zero value if x is nil or %[2]s is absent or null.\n", p.typ, p.field)
			g.write("func (x *%s) Get%s() %s {\nif x == nil {\nvar zero %s\nreturn zero\n}\nv, _ := x.%s.Get()\nreturn v\n}\n\n", p.typ, p.field, p.elem, p.elem, p.field)
			continue
		}
		if p.object {
			g.write("// Get%[2]s returns %[2]s, or nil if x is nil.\n", p.typ, p.field)
			g.write("func (x *%s) Get%s() *%s {\nif x == nil {\nreturn nil\n}\nreturn x.%s\n}\n\n", p.typ, p.field, p.elem, p.field)
			continue
		}
		g.write("// Get%[2]s returns the value of %[2]s, or the zero value if x or %[2]s is nil.\n", p.typ, p.field)
		g.write("func (x *%s) Get%s() %s {\nif x != nil && x.%s != nil {\nreturn *x.%s\n}\nvar zero %s\nreturn zero\n}\n\n", p.typ, p.field, p.elem, p.field, p.field, p.elem)
	}
}

// isStruct reports whether a field of schema type t is emitted as a struct,
// following references through aliases.
func (g *generator) isStruct(t *tsdef.Type) bool {
	for {
		t, _ = t.NonNull()
		if t.Kind != tsdef.KindRef {
			break
		}
		if t = g.defs[t.Name]; t == nil {
			return false
		}
	}
	f, _, err := g.form(t)
	return err == nil && f == formStruct
}
