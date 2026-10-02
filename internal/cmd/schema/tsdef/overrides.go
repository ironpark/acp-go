package tsdef

import (
	"bytes"
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Overrides are corrections the generator applies to the TypeScript schema
// where it says less than the protocol means.
type Overrides struct {
	// Numbers gives the Go type of numbers the TypeScript schema leaves
	// unconstrained, keyed in TypeScript names by "Type.member" for a member,
	// or by "Type" for a definition that is itself a number or has one number
	// alternative: the protocol's Rust schema declares them integers.
	Numbers map[string]string `yaml:"numbers"`
}

// numberTypes are the Go types a number override may choose.
var numberTypes = []string{"int32", "int64", "uint32", "uint64"}

// ParseOverrides reads overrides from YAML, rejecting unknown keys and types.
func ParseOverrides(data []byte) (*Overrides, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var o Overrides
	if err := dec.Decode(&o); err != nil {
		return nil, fmt.Errorf("overrides: %w", err)
	}
	for key, typ := range o.Numbers {
		if !slices.Contains(numberTypes, typ) {
			return nil, fmt.Errorf("overrides: numbers.%s: %q is not one of %v", key, typ, numberTypes)
		}
		if name, member, ok := strings.Cut(key, "."); name == "" || ok && member == "" {
			return nil, fmt.Errorf("overrides: numbers.%s: want Type or Type.member", key)
		}
	}
	return &o, nil
}

// Apply applies the overrides that name a definition or member of schema and
// returns their keys. A schema version need not have every overridden name,
// but one it has must be a number or a union with exactly one number, and one
// the Zod schema has not already made an integer.
func (o *Overrides) Apply(schema *Schema) (map[string]bool, error) {
	defs := map[string]*Type{}
	for _, d := range schema.Types {
		defs[d.Name] = d.Type
	}
	applied := map[string]bool{}
	for key, typ := range o.Numbers {
		name, member, isMember := strings.Cut(key, ".")
		t := defs[name]
		if t == nil {
			continue
		}
		var targets []*Type
		if !isMember {
			targets = []*Type{t}
		} else if t.Kind == KindObject {
			for i := range t.Fields {
				if t.Fields[i].Name == member {
					targets = append(targets, t.Fields[i].Type)
				}
			}
		}
		for _, target := range targets {
			number := numberMember(target)
			if number == nil {
				return nil, fmt.Errorf("overrides: numbers.%s: %s has no single number", key, target.Kind)
			}
			if number.Number != "" {
				return nil, fmt.Errorf("overrides: numbers.%s: the Zod schema already makes it %s", key, number.Number)
			}
			number.Number = typ
			applied[key] = true
		}
	}
	return applied, nil
}

// numberMember returns t when it is a number, or its one number alternative
// when it is a union.
func numberMember(t *Type) *Type {
	if t.Kind == KindNumber {
		return t
	}
	var number *Type
	if t.Kind == KindUnion {
		for _, m := range t.Members {
			if m.Kind == KindNumber {
				if number != nil {
					return nil
				}
				number = m
			}
		}
	}
	return number
}
