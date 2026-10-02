// Package tsdef reads the TypeScript subset used by ACP's generated schemas.
// It does not execute TypeScript or Zod code.
package tsdef

import "strconv"

// Schema is independent of the parser's C-owned syntax tree.
type Schema struct {
	Validators map[string]*Zod
	Types      []Definition
	Constants  []Constant
}

type Definition struct {
	Name    string
	Comment string
	Type    *Type
}

// Kind is the shape of a [Type].
type Kind uint8

const (
	KindInvalid Kind = iota
	KindRef          // Name is another definition

	// TypeScript's primitive types.
	KindString
	KindNumber
	KindBoolean
	KindUnknown
	KindAny
	KindNever
	KindNull

	KindLiteral      // Literal is the value as written, strings quoted
	KindUnion        // Members, nested unions flattened
	KindIntersection // Members, nested intersections flattened
	KindArray        // Element
	KindObject       // Fields, and Element for a string index signature
)

var kindNames = [...]string{
	KindInvalid: "invalid", KindRef: "ref",
	KindString: "string", KindNumber: "number", KindBoolean: "boolean", KindUnknown: "unknown",
	KindAny: "any", KindNever: "never", KindNull: "null",
	KindLiteral: "literal", KindUnion: "union", KindIntersection: "intersection",
	KindArray: "array", KindObject: "object",
}

func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "Kind(" + strconv.Itoa(int(k)) + ")"
}

// Type retains unions and intersections rather than flattening away wire semantics.
type Type struct {
	Number  string // Go integer representation inferred from Zod numeric builders.
	Kind    Kind
	Name    string
	Literal string
	Members []*Type
	Fields  []Field
	Element *Type
}

type Field struct {
	Name     string
	Comment  string
	Optional bool
	Type     *Type
	// Tristate marks an optional, nullable member whose absence and null
	// mean different things; see [Overrides.Tristate].
	Tristate bool
}

type Constant struct {
	Name    string
	Value   string
	Members []Constant
}

// Field returns the member of object t with the given JSON name, or nil.
func (t *Type) Field(name string) *Field {
	for i := range t.Fields {
		if t.Fields[i].Name == name {
			return &t.Fields[i]
		}
	}
	return nil
}

// NonNull returns t without its null member, and whether it had one; a lone
// null reports itself.
func (t *Type) NonNull() (*Type, bool) {
	if t.Kind != KindUnion {
		return t, t.Kind == KindNull
	}
	var members []*Type
	null := false
	for _, m := range t.Members {
		if m.Kind == KindNull {
			null = true
		} else {
			members = append(members, m)
		}
	}
	if len(members) == 1 {
		return members[0], null
	}
	return &Type{Kind: KindUnion, Members: members}, null
}
