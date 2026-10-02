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
	// Tristate lists, per schema version, the optional nullable members
	// where absent and null mean different things, as "Type.member" in
	// TypeScript names: the protocol's Rust schema declares them
	// MaybeUndefined.
	Tristate map[string][]string `yaml:"tristate"`
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
	for version, keys := range o.Tristate {
		for _, key := range keys {
			if name, member, ok := strings.Cut(key, "."); !ok || name == "" || member == "" {
				return nil, fmt.Errorf("overrides: tristate.%s %s: want Type.member", version, key)
			}
		}
	}
	return &o, nil
}

// Apply applies the overrides to schema, the given version of the protocol,
// and returns the keys of the number overrides that named something in it;
// every tristate member listed for the version must exist. A schema version need not have every overridden name,
// but one it has must be a number or a union with exactly one number, and one
// the Zod schema has not already made an integer.
func (o *Overrides) Apply(schema *Schema, version string) (map[string]bool, error) {
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
	for _, key := range o.Tristate[version] {
		name, member, _ := strings.Cut(key, ".")
		var f *Field
		if t := defs[name]; t != nil && t.Kind == KindObject {
			f = t.Field(member)
		}
		if f == nil {
			return nil, fmt.Errorf("overrides: tristate.%s %s names no member of that schema version", version, key)
		}
		value, nullable := f.Type.NonNull()
		if !f.Optional || !nullable {
			return nil, fmt.Errorf("overrides: tristate.%s %s: member is not optional and nullable", version, key)
		}
		if value.Kind == KindUnknown || value.Kind == KindAny {
			return nil, fmt.Errorf("overrides: tristate.%s %s: raw JSON already tells null from absent", version, key)
		}
		f.Tristate = true
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
