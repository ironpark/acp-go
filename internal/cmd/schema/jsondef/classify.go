package jsondef

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// GenerateType represents the type of Go construct to generate from a JSON schema definition.
type GenerateType int

const (
	Unknown GenerateType = iota
	Enum
	ComplexStruct
	Struct
	Primitive
	Array
	Ref
	Union
)

func (t GenerateType) String() string {
	return []string{"Unknown", "Enum", "ComplexStruct", "Struct", "Primitive", "Array", "Ref", "Union"}[t]
}

// Definition represents a JSON schema definition with its corresponding Go type.
type Definition struct {
	Name     string
	TypeName string
	Type     GenerateType
	Nullable bool
	Schema   *Schema
}

// GetDescription returns the schema description or empty string.
func (d Definition) GetDescription() string {
	if d.Schema.Description != nil {
		return *d.Schema.Description
	}
	return ""
}

// GetFieldType returns the Go type string for this definition.
func (d Definition) GetFieldType() string {
	if d.TypeName != "" {
		if d.Nullable && !strings.HasPrefix(d.TypeName, "*") {
			return "*" + d.TypeName
		}
		return d.TypeName
	}

	switch d.Type {
	case Ref:
		ref := findRef(d.Schema)
		if ref != "" {
			typeName := ResolveRefGo(ref)
			if d.Nullable {
				return "*" + typeName
			}
			return typeName
		}
		return "unknown"
	case Array:
		if d.Schema.Items != nil {
			itemDef := Classify(d.Schema.Items.Ref, d.Schema.Items)
			return "[]" + itemDef.GetFieldType()
		}
		return "[]any"
	case Primitive:
		typeName := GetPrimitiveGoType(d.Schema)
		if d.Nullable && typeName != "string" && typeName != "any" {
			return "*" + typeName
		}
		return typeName
	}
	return "unknown"
}

// IsRequired checks if propName is in the schema's required list.
func (d Definition) IsRequired(propName string) bool {
	return slices.Contains(d.Schema.Required, propName)
}

func findRef(schema *Schema) string {
	// Direct allOf wrapping: {allOf: [{$ref: ...}]}
	if len(schema.AllOf) == 1 && schema.AllOf[0].Ref != "" {
		return schema.AllOf[0].Ref
	}
	for _, s := range schema.AnyOf {
		if s.Ref != "" {
			return s.Ref
		}
		// Handle allOf wrapping: anyOf[{allOf: [{$ref: ...}]}]
		if len(s.AllOf) == 1 && s.AllOf[0].Ref != "" {
			return s.AllOf[0].Ref
		}
	}
	for _, s := range schema.OneOf {
		if s.Ref != "" {
			return s.Ref
		}
		if len(s.AllOf) == 1 && s.AllOf[0].Ref != "" {
			return s.AllOf[0].Ref
		}
	}
	return ""
}

// GetDefinitions extracts and classifies all definitions from a schema's $defs.
func GetDefinitions(schema *Schema) []Definition {
	var definitions []Definition
	for name, s := range schema.Defs {
		definitions = append(definitions, Classify(name, s))
	}
	sort.Slice(definitions, func(i, j int) bool {
		if definitions[i].Type == definitions[j].Type {
			return definitions[i].Name < definitions[j].Name
		}
		return definitions[i].Type < definitions[j].Type
	})
	return definitions
}

// Classify determines the GenerateType for a JSON schema definition.
func Classify(name string, schema *Schema) Definition {
	genType, nullable := classifyType(schema)
	def := Definition{
		Name:     name,
		Type:     genType,
		Nullable: nullable,
		Schema:   schema,
	}

	if schema.Ref != "" {
		def.TypeName = ResolveRefGo(schema.Ref)
	}

	// If classified as Struct but no properties, try to unwrap from anyOf/oneOf
	if def.Type == Struct && schema.Properties == nil {
		notNullTypes := FilterNonNull(schema.AnyOf)
		if len(notNullTypes) == 1 {
			def.Schema = notNullTypes[0]
		} else {
			notNullTypes = FilterNonNull(schema.OneOf)
			if len(notNullTypes) == 1 {
				def.Schema = notNullTypes[0]
			} else {
				fmt.Printf("schema.Properties is nil for %s\n", name)
			}
		}
	}

	return def
}

func classifyType(s *Schema) (genType GenerateType, nullable bool) {
	nullable = IsNullable(s)

	switch len(s.Type) {
	case 0:
		if len(s.OneOf) > 0 {
			if len(s.OneOf) == 1 && s.OneOf[0].Ref != "" {
				return Ref, false
			}
			allString := true
			for _, oneOf := range s.OneOf {
				if !IsString(oneOf) || oneOf.Const == nil {
					allString = false
				}
			}
			if allString {
				return Enum, false
			}
			if IsUnionOfRefs(s.OneOf) {
				return Union, nullable
			}
			return ComplexStruct, false
		}
		if len(s.AnyOf) > 0 {
			notNullTypes := FilterNonNull(s.AnyOf)
			if len(notNullTypes) == 0 {
				return Unknown, false
			}
			if len(notNullTypes) == 1 {
				if notNullTypes[0].Ref != "" {
					return Ref, false
				}
				// Handle allOf wrapping: anyOf[{allOf: [{$ref: ...}]}]
				if len(notNullTypes[0].AllOf) == 1 && notNullTypes[0].AllOf[0].Ref != "" {
					return Ref, nullable
				}
				gt, _ := classifyType(notNullTypes[0])
				return gt, nullable
			}
			if IsUnionOfRefs(s.AnyOf) {
				return Union, nullable
			}
			// Check if anyOf is a const-based enum (string or integer)
			if isConstEnum(notNullTypes) {
				return Enum, false
			}
			// Check if all non-null types are different primitives (e.g., int | string)
			if isPrimitiveUnion(notNullTypes) {
				return Primitive, nullable
			}
			return ComplexStruct, false
		}
	case 1:
		defType := s.Type[0]
		if defType == "object" {
			if s.Properties == nil && s.AdditionalProperties != nil && s.AdditionalProperties.Schema != nil {
				return Primitive, false
			}
			// An object with a discriminator + oneOf/anyOf is a discriminated union,
			// not a plain struct. Classify as ComplexStruct so the generator produces
			// the proper variant interface, As* accessors, and JSON marshalling.
			if s.Discriminator != nil && (len(s.OneOf) > 0 || len(s.AnyOf) > 0) {
				return ComplexStruct, false
			}
			return Struct, false
		}
		if defType == "array" {
			return Array, nullable
		}
		if len(s.Enum) > 0 {
			return Enum, false
		}
		return Primitive, false
	case 2:
		typeName := ""
		for _, t := range s.Type {
			if t != "null" {
				typeName = t
				break
			}
		}
		if typeName == "object" {
			if s.Properties == nil && s.AdditionalProperties != nil && s.AdditionalProperties.Schema != nil {
				return Primitive, nullable
			}
			return Struct, nullable
		}
		if typeName == "array" {
			return Array, nullable
		}
		return Primitive, nullable
	}

	if s.Ref != "" {
		return Ref, false
	}
	// Handle allOf with single $ref (wrapper pattern)
	if len(s.AllOf) == 1 && s.AllOf[0].Ref != "" && s.Properties == nil {
		return Ref, false
	}
	return Unknown, false
}

// isConstEnum checks if schemas represent a const-based enum pattern.
// Allows one catch-all variant without const (e.g., ErrorCode has 7 consts + 1 catch-all integer).
func isConstEnum(schemas []*Schema) bool {
	if len(schemas) < 2 {
		return false
	}
	constCount := 0
	for _, s := range schemas {
		if s.Const != nil {
			constCount++
		}
	}
	// At least 2 const values, and at most 1 catch-all
	return constCount >= 2 && constCount >= len(schemas)-1
}

// isPrimitiveUnion checks if all schemas are primitive types (not object/array).
func isPrimitiveUnion(schemas []*Schema) bool {
	if len(schemas) < 2 {
		return false
	}
	for _, s := range schemas {
		if len(s.Type) == 0 || s.Type[0] == "object" || s.Type[0] == "array" {
			return false
		}
	}
	return true
}
