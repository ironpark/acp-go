package main

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/ironpark/go-acp/internal/cmd/schema/astgen"
	"github.com/ironpark/go-acp/internal/cmd/schema/jsondef"
)

const (
	DefaultFilePermissions = 0644
	EmptyEnumName          = "Empty"
	JSONRawMessageType     = "json.RawMessage"
)

// OneOfVariant represents a discriminated union variant.
type OneOfVariant struct {
	FieldName string // field name for accessor (e.g., "text", "Stdio")
	TypeName  string // PascalCase type name
	DiscValue string // discriminator value (empty for default variant)
	DirectRef bool   // true if using ref type directly (no wrapper struct)
}

// Generator handles schema to Go code generation.
type Generator struct {
	config           *Config
	schema           *jsondef.Schema
	metadata         *Metadata
	builder          *astgen.FileBuilder
	generatedCode    []byte
	skippedItems     []string
	excludedDefNames map[string]bool   // definitions to exclude (from base schema)
	baseSchema       *jsondef.Schema   // base schema for comparing union variants
}

// NewGenerator creates a new generator instance.
func NewGenerator(config *Config) *Generator {
	builder := astgen.NewFileBuilder(config.PackageName)
	builder.SetGeneratedBy("schema-gen")
	return &Generator{
		config:  config,
		builder: builder,
	}
}

// LoadSchema loads JSON schema from input file.
func (g *Generator) LoadSchema() error {
	schema, err := jsondef.LoadSchema(g.config.InputFile)
	if err != nil {
		return err
	}
	g.schema = schema

	// Merge additional properties from extended schema if configured.
	// This allows the base target to pick up new properties that the
	// extended schema adds to types already defined in the base schema
	// (e.g., unstable properties on stable types).
	if g.config.MergePropertiesFrom != "" {
		if err := g.mergeExtendedProperties(); err != nil {
			return fmt.Errorf("failed to merge properties from extended schema: %w", err)
		}
	}

	// Build exclude set from base schema if configured
	if g.config.ExcludeFrom != "" {
		if err := g.loadExcludedDefs(); err != nil {
			return fmt.Errorf("failed to load base schema for exclusion: %w", err)
		}
	}
	return nil
}

// loadExcludedDefs loads the base schema and collects all definition names
// that exist in it, so the current target only generates truly new types.
func (g *Generator) loadExcludedDefs() error {
	baseSchema, err := jsondef.LoadSchema(g.config.ExcludeFrom)
	if err != nil {
		return err
	}
	g.baseSchema = baseSchema

	g.excludedDefNames = make(map[string]bool, len(baseSchema.Defs))
	for name := range baseSchema.Defs {
		g.excludedDefNames[name] = true
	}
	return nil
}

// mergeExtendedProperties loads an extended schema (e.g., the unstable schema)
// and merges any new properties it defines on types that also exist in the base
// schema. This allows the base output to include fields added by the extended
// schema without requiring manual edits to the generated file.
func (g *Generator) mergeExtendedProperties() error {
	extSchema, err := jsondef.LoadSchema(g.config.MergePropertiesFrom)
	if err != nil {
		return err
	}

	for name, extDef := range extSchema.Defs {
		baseDef, exists := g.schema.Defs[name]
		if !exists {
			// Type only exists in extended schema; not our concern here.
			continue
		}
		if extDef.Properties == nil || baseDef.Properties == nil {
			continue
		}
		// Merge any properties from the extended def that are not in the base def.
		for propName, propSchema := range extDef.Properties {
			if _, alreadyExists := baseDef.Properties[propName]; !alreadyExists {
				baseDef.Properties[propName] = propSchema
				fmt.Printf("Merged property %q from extended schema into %s\n", propName, name)
			}
		}
	}

	return nil
}

// LoadMetadata loads metadata from meta.json file if provided.
func (g *Generator) LoadMetadata() error {
	if g.config.MetaFile == "" {
		return nil
	}
	meta, err := LoadMetadata(g.config.MetaFile)
	if err != nil {
		return fmt.Errorf("failed to load metadata: %w", err)
	}
	g.metadata = meta
	return nil
}

// Generate generates Go code from the loaded schema.
func (g *Generator) Generate() error {
	if g.schema == nil {
		return fmt.Errorf("schema not loaded, call LoadSchema() first")
	}

	definitions := jsondef.GetDefinitions(g.schema)

	for _, definition := range definitions {
		if g.isTypeIgnored(definition.Name) {
			// For excluded discriminated unions, check if the current schema
			// has new variants not present in the base schema. If so, generate
			// only the new variant wrapper structs and their marker methods.
			if g.baseSchema != nil && definition.Type == jsondef.ComplexStruct {
				defName := definition.Name
				definition.Name = toTitleCase(definition.Name)
				if err := g.generateExtendedUnionVariants(definition); err != nil {
					if g.config.IgnoreErrors {
						fmt.Printf("Warning: Skipping extended variants for %s: %v\n", defName, err)
					} else {
						return fmt.Errorf("failed to generate extended variants for %s: %w", defName, err)
					}
				}
			}
			g.addSkippedItem(definition.Name)
			continue
		}

		// Apply Go naming conventions to definition names
		definition.Name = toTitleCase(definition.Name)

		if err := g.generateDefinition(definition); err != nil {
			if g.config.IgnoreErrors {
				fmt.Printf("Warning: Skipping definition %s due to error: %v\n", definition.Name, err)
				g.addSkippedItem(definition.Name)
				continue
			}
			return fmt.Errorf("failed to generate definition %s: %w", definition.Name, err)
		}
	}

	if len(g.skippedItems) > 0 {
		fmt.Printf("Successfully generated types, skipped %d definitions: %v\n",
			len(g.skippedItems), g.skippedItems)
	}

	// Generate constants from metadata
	if g.metadata != nil {
		g.generateConstants()
	}

	return nil
}

// SaveToFile saves the generated code to output file.
func (g *Generator) SaveToFile() error {
	data, err := g.builder.Build()
	if err != nil {
		return fmt.Errorf("failed to build code: %w", err)
	}

	if g.config.OutputFile == "" {
		_, err := os.Stdout.Write(data)
		return err
	}

	if err := os.WriteFile(g.config.OutputFile, data, DefaultFilePermissions); err != nil {
		return fmt.Errorf("failed to write output file: %w", err)
	}
	return nil
}

// GetGeneratedContent returns the generated code as bytes.
func (g *Generator) GetGeneratedContent() []byte {
	if g.generatedCode != nil {
		return g.generatedCode
	}
	data, err := g.builder.Build()
	if err != nil {
		return nil
	}
	g.generatedCode = data
	return data
}

// GetSkippedCount returns the number of skipped definitions.
func (g *Generator) GetSkippedCount() int {
	return len(g.skippedItems)
}

// GetSkippedItems returns the list of skipped definition names.
func (g *Generator) GetSkippedItems() []string {
	return g.skippedItems
}

// generateDefinition generates code for a single definition.
func (g *Generator) generateDefinition(def jsondef.Definition) error {
	switch def.Type {
	case jsondef.Primitive:
		return g.generatePrimitive(def)
	case jsondef.Enum:
		return g.generateEnum(def.Name, def.Schema)
	case jsondef.Struct:
		desc := def.GetDescription()
		return g.generateStruct(def.Name, def.Schema, desc)
	case jsondef.ComplexStruct:
		return g.generateComplexStruct(def.Name, def.Schema)
	case jsondef.Ref:
		return g.generateTypeAlias(def)
	case jsondef.Union:
		return g.generateUnion(def)
	default:
		return fmt.Errorf("unsupported definition type: %s", def.Type)
	}
}

// generatePrimitive generates a primitive type definition.
func (g *Generator) generatePrimitive(def jsondef.Definition) error {
	comment := def.GetDescription()
	decl := astgen.TypeDef(def.Name, def.GetFieldType(), comment)
	g.builder.AddDecl(decl)
	return nil
}

// generateEnum generates an enum type with constants.
func (g *Generator) generateEnum(name string, schema *jsondef.Schema) error {
	enumValues := g.extractEnumValues(schema)
	if len(enumValues) == 0 {
		return fmt.Errorf("no enum values found for %s", name)
	}

	// Detect base type: if any value is not quoted, it's an integer enum
	baseType := "string"
	for _, ev := range enumValues {
		if !strings.HasPrefix(ev.Value, `"`) {
			baseType = "int"
			break
		}
	}

	// Create type definition
	comment := ""
	if schema.Description != nil {
		comment = *schema.Description
	}
	g.builder.AddDecl(astgen.TypeDef(name, baseType, comment))

	// Create const block
	consts := make([]astgen.ConstEntry, 0, len(enumValues))
	for _, ev := range enumValues {
		consts = append(consts, astgen.ConstEntry{
			Name:    name + ev.Name,
			Type:    name,
			Value:   ev.Value,
			Comment: ev.Comment,
		})
	}
	g.builder.AddRawDecl(astgen.ConstBlockSource(consts))

	return nil
}

type enumValue struct {
	Name    string
	Value   string // pre-formatted: `"str"` for strings, `123` for ints
	Comment string
}

func (g *Generator) extractEnumValues(schema *jsondef.Schema) []enumValue {
	var values []enumValue

	// Handle Enum field
	for _, raw := range schema.Enum {
		var str string
		if err := rawToString(raw, &str); err == nil {
			enumName := toTitleCase(str)
			if enumName == "" {
				enumName = EmptyEnumName
			}
			values = append(values, enumValue{Name: enumName, Value: `"` + str + `"`})
		}
	}

	// Handle OneOf field
	for _, oneOf := range schema.OneOf {
		if oneOf.Const != nil {
			if str, ok := oneOf.Const.StringValue(); ok {
				enumName := toTitleCase(str)
				if enumName == "" {
					enumName = EmptyEnumName
				}
				comment := ""
				if oneOf.Description != nil {
					comment = *oneOf.Description
				}
				values = append(values, enumValue{Name: enumName, Value: `"` + str + `"`, Comment: comment})
			}
		}
	}

	// Handle AnyOf field (const-based enums, e.g., ErrorCode, SessionConfigOptionCategory)
	for _, anyOf := range schema.AnyOf {
		if jsondef.IsNullType(anyOf) {
			continue
		}
		if anyOf.Const == nil {
			continue
		}
		comment := ""
		if anyOf.Description != nil {
			comment = *anyOf.Description
		}
		if str, ok := anyOf.Const.StringValue(); ok {
			enumName := toTitleCase(str)
			if enumName == "" {
				enumName = EmptyEnumName
			}
			values = append(values, enumValue{Name: enumName, Value: `"` + str + `"`, Comment: comment})
		} else if num, ok := anyOf.Const.Value.(float64); ok {
			intVal := int(num)
			enumName := g.intEnumName(anyOf)
			values = append(values, enumValue{Name: enumName, Value: fmt.Sprintf("%d", intVal), Comment: comment})
		}
	}

	return values
}

// intEnumName derives a Go constant name for an integer enum value.
func (g *Generator) intEnumName(schema *jsondef.Schema) string {
	if schema.Title != "" {
		return toTitleCase(schema.Title)
	}
	if schema.Description != nil {
		desc := *schema.Description
		// Use first line of description if it's short enough
		if idx := strings.Index(desc, "."); idx > 0 {
			desc = desc[:idx]
		}
		if len(desc) < 40 {
			return toTitleCase(desc)
		}
	}
	// Fallback: use the number
	if num, ok := schema.Const.Value.(float64); ok {
		intVal := int(num)
		if intVal < 0 {
			return fmt.Sprintf("Neg%d", -intVal)
		}
		return fmt.Sprintf("Code%d", intVal)
	}
	return "Unknown"
}

func rawToString(raw []byte, s *string) error {
	if len(raw) >= 2 && raw[0] == '"' {
		return json.Unmarshal(raw, s)
	}
	return fmt.Errorf("not a string")
}

// generateStruct generates a struct type definition.
func (g *Generator) generateStruct(name string, schema *jsondef.Schema, comment string) error {
	if schema.Properties == nil {
		if len(schema.AnyOf) > 0 || len(schema.OneOf) > 0 {
			// Union type that couldn't be fully resolved — use any
			g.builder.AddDecl(astgen.TypeDef(name, "any", comment))
		} else {
			g.builder.AddDecl(astgen.EmptyStructDef(name, comment))
		}
		return nil
	}

	fields, err := g.buildStructFields(schema)
	if err != nil {
		return err
	}

	g.builder.AddDecl(astgen.StructDef(name, fields, comment))
	return nil
}

func (g *Generator) buildStructFields(schema *jsondef.Schema) ([]astgen.StructField, error) {
	// Sort property names for consistent ordering
	propNames := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		propNames = append(propNames, name)
	}
	slices.Sort(propNames)

	var fields []astgen.StructField
	for _, propName := range propNames {
		// Skip _meta property — handled as a generic map field
		if propName == "_meta" {
			fields = append(fields, astgen.StructField{
				Name: "Meta",
				Type: astgen.TypeExpr("map[string]any"),
				Tag:  `json:"_meta,omitempty"`,
			})
			continue
		}
		propSchema := schema.Properties[propName]
		def := jsondef.Classify(propName, propSchema)

		field, err := g.buildStructField(propName, propSchema, def, schema)
		if err != nil {
			return nil, err
		}
		fields = append(fields, field)
	}

	return fields, nil
}

func (g *Generator) buildStructField(propName string, propSchema *jsondef.Schema, def jsondef.Definition, parentSchema *jsondef.Schema) (astgen.StructField, error) {
	fieldName := toTitleCase(propName)
	isOptional := g.isFieldOptional(propName, parentSchema, def)
	jsonTag := propName
	if isOptional {
		jsonTag += ",omitempty"
	}
	tag := `json:"` + jsonTag + `"`

	var fieldType string

	switch def.Type {
	case jsondef.Primitive:
		fieldType = jsondef.GetGoTypeName(propSchema)
	case jsondef.Struct:
		// Generate nested struct
		nestedComment := ""
		if propSchema.Description != nil {
			nestedComment = *propSchema.Description
		}
		if err := g.generateStruct(propName, propSchema, nestedComment); err != nil {
			return astgen.StructField{}, fmt.Errorf("failed to generate nested struct %s: %w", propName, err)
		}
		fieldType = jsondef.GetGoTypeName(propSchema)
	case jsondef.Array:
		fieldType = def.GetFieldType()
	case jsondef.Ref:
		fieldType = def.GetFieldType()
		if isOptional && !strings.HasPrefix(fieldType, "*") {
			fieldType = "*" + fieldType
		}
	default:
		// No type info → json.RawMessage
		if isNoTypeSchema(propSchema) {
			fieldType = JSONRawMessageType
			g.builder.AddImport("encoding/json")
		} else {
			return astgen.StructField{}, fmt.Errorf("unsupported field type %s for property %s", def.Type, propName)
		}
	}

	return astgen.StructField{
		Name: fieldName,
		Type: astgen.TypeExpr(fieldType),
		Tag:  tag,
	}, nil
}

func (g *Generator) isFieldOptional(propName string, schema *jsondef.Schema, def jsondef.Definition) bool {
	if def.Nullable {
		return true
	}
	if schema.Required != nil {
		return !slices.Contains(schema.Required, propName)
	}
	return true
}

func isNoTypeSchema(schema *jsondef.Schema) bool {
	return len(schema.Type) == 0 &&
		schema.Ref == "" &&
		schema.Properties == nil &&
		schema.AnyOf == nil &&
		schema.OneOf == nil
}

// generateTypeAlias generates a type alias for single reference types.
func (g *Generator) generateTypeAlias(def jsondef.Definition) error {
	targetType := def.GetFieldType()
	comment := def.GetDescription()
	g.builder.AddDecl(astgen.TypeAlias(def.Name, targetType, comment))
	return nil
}

// generateUnion generates a union type (any) for types without discriminator.
func (g *Generator) generateUnion(def jsondef.Definition) error {
	schema := def.Schema
	allVariants := make([]*jsondef.Schema, 0, len(schema.AnyOf)+len(schema.OneOf))
	allVariants = append(allVariants, schema.AnyOf...)
	allVariants = append(allVariants, schema.OneOf...)

	// Extract type names from $ref
	var typeNames []string
	hasNull := false
	for _, variant := range allVariants {
		if variant.Ref != "" {
			typeNames = append(typeNames, jsondef.ResolveRefGo(variant.Ref))
		} else if jsondef.IsNullType(variant) {
			hasNull = true
		}
	}

	// Build comment
	var commentLines []string
	if schema.Description != nil {
		commentLines = append(commentLines, *schema.Description)
		commentLines = append(commentLines, "")
	}
	if len(typeNames) > 0 || hasNull {
		commentLines = append(commentLines, "Possible types:")
		for _, tn := range typeNames {
			commentLines = append(commentLines, "- "+tn)
		}
		if hasNull {
			commentLines = append(commentLines, "- null")
		}
	}

	comment := strings.Join(commentLines, "\n")
	g.builder.AddDecl(astgen.TypeDef(def.Name, "any", comment))
	return nil
}

// generateComplexStruct generates a discriminated union using marker interface pattern.
func (g *Generator) generateComplexStruct(name string, schema *jsondef.Schema) error {
	allVariants := make([]*jsondef.Schema, 0, len(schema.AnyOf)+len(schema.OneOf))
	allVariants = append(allVariants, schema.AnyOf...)
	allVariants = append(allVariants, schema.OneOf...)

	// Use explicit discriminator if available, otherwise detect
	var discriminatorField string
	if schema.Discriminator != nil && schema.Discriminator.PropertyName != "" {
		discriminatorField = schema.Discriminator.PropertyName
	} else {
		discriminatorField = jsondef.DetectDiscriminator(allVariants)
	}

	if discriminatorField == "" {
		// No discriminator found — treat as regular struct or fallback
		comment := ""
		if schema.Description != nil {
			comment = *schema.Description
		}
		return g.generateStruct(name, schema, comment)
	}

	// Collect variants
	variants := g.collectVariants(name, schema, allVariants, discriminatorField)
	if len(variants) == 0 {
		comment := ""
		if schema.Description != nil {
			comment = *schema.Description
		}
		return g.generateStruct(name, schema, comment)
	}

	// Generate marker interface
	markerName := strings.ToLower(name[:1]) + name[1:] + "Variant"
	markerMethodName := "is" + name + "Variant"

	// Generate marker interface method on each variant struct
	for _, v := range variants {
		methodSrc := fmt.Sprintf(`func (%s) %s() string { return "%s" }`,
			v.TypeName, markerMethodName, v.DiscValue)
		fn, err := astgen.CreateMethodFromSource(methodSrc)
		if err != nil {
			return fmt.Errorf("failed to create marker method for %s: %w", v.TypeName, err)
		}
		g.builder.AddDecl(fn)
	}

	// Generate marker interface type
	interfaceSrc := fmt.Sprintf(`type %s interface { %s() string }`, markerName, markerMethodName)
	decls, err := astgen.CreateDeclsFromSource(interfaceSrc)
	if err != nil {
		return fmt.Errorf("failed to create marker interface: %w", err)
	}
	for _, d := range decls {
		g.builder.AddDecl(d)
	}

	// Generate union struct
	comment := ""
	if schema.Description != nil {
		comment = *schema.Description
	}
	g.builder.AddDecl(astgen.StructDef(name, []astgen.StructField{
		{Name: "variant", Type: astgen.Ident(markerName)},
	}, comment))

	// Check if any variant uses direct ref (needs discriminator injection in marshal)
	hasDirectRef := false
	for _, v := range variants {
		if v.DirectRef {
			hasDirectRef = true
			break
		}
	}

	// Generate MarshalJSON
	g.builder.AddImport("encoding/json")
	g.builder.AddImport("fmt")
	if hasDirectRef {
		g.generateMarshalJSONWithDiscriminator(name, discriminatorField)
	} else {
		g.generateMarshalJSON(name, variants)
	}

	// Generate UnmarshalJSON
	g.generateUnmarshalJSON(name, variants, discriminatorField)

	// Generate As* accessor methods
	g.generateAccessors(name, variants)

	return nil
}

// generateExtendedUnionVariants generates only the new variant structs and marker
// methods for a discriminated union that exists in the base schema but has
// additional variants in the current (unstable) schema. It also generates an
// init() function that registers the new variants for deserialization.
func (g *Generator) generateExtendedUnionVariants(def jsondef.Definition) error {
	schema := def.Schema
	baseDef, baseExists := g.baseSchema.Defs[strings.ToLower(def.Name[:1])+def.Name[1:]]
	if !baseExists {
		// Try the original name before title-casing
		for name, s := range g.baseSchema.Defs {
			if toTitleCase(name) == def.Name {
				baseDef = s
				baseExists = true
				break
			}
		}
	}
	if !baseExists {
		return nil // Not in base schema, nothing to extend
	}

	// Only extend if the base definition is also a discriminated union (ComplexStruct).
	// If the base type is e.g. a Ref/alias, it doesn't have the variant registry.
	baseDefined := jsondef.Classify("", baseDef)
	if baseDefined.Type != jsondef.ComplexStruct {
		return nil
	}

	// Determine discriminator field
	allVariants := make([]*jsondef.Schema, 0, len(schema.AnyOf)+len(schema.OneOf))
	allVariants = append(allVariants, schema.AnyOf...)
	allVariants = append(allVariants, schema.OneOf...)

	var discriminatorField string
	if schema.Discriminator != nil && schema.Discriminator.PropertyName != "" {
		discriminatorField = schema.Discriminator.PropertyName
	} else {
		discriminatorField = jsondef.DetectDiscriminator(allVariants)
	}
	if discriminatorField == "" {
		return nil
	}

	// Collect base variant discriminator values
	baseAllVariants := make([]*jsondef.Schema, 0, len(baseDef.AnyOf)+len(baseDef.OneOf))
	baseAllVariants = append(baseAllVariants, baseDef.AnyOf...)
	baseAllVariants = append(baseAllVariants, baseDef.OneOf...)
	baseDiscValues := make(map[string]bool)
	for _, v := range baseAllVariants {
		if v.Properties != nil {
			if dp, ok := v.Properties[discriminatorField]; ok && dp.Const != nil {
				if val, ok := dp.Const.StringValue(); ok {
					baseDiscValues[val] = true
				}
			}
		}
	}

	// Filter to only new variants
	var newVariants []*jsondef.Schema
	for _, v := range allVariants {
		if v.Properties != nil {
			if dp, ok := v.Properties[discriminatorField]; ok && dp.Const != nil {
				if val, ok := dp.Const.StringValue(); ok {
					if !baseDiscValues[val] {
						newVariants = append(newVariants, v)
					}
				}
			}
		}
	}

	if len(newVariants) == 0 {
		return nil
	}

	// Generate the new variant structs and marker methods
	name := def.Name
	markerMethodName := "is" + name + "Variant"
	variants := g.collectVariants(name, schema, newVariants, discriminatorField)

	for _, v := range variants {
		methodSrc := fmt.Sprintf(`func (%s) %s() string { return "%s" }`,
			v.TypeName, markerMethodName, v.DiscValue)
		fn, err := astgen.CreateMethodFromSource(methodSrc)
		if err != nil {
			return fmt.Errorf("failed to create marker method for %s: %w", v.TypeName, err)
		}
		g.builder.AddDecl(fn)
	}

	// Generate init() function to register new variants
	g.builder.AddImport("encoding/json")
	var registrations []string
	for _, v := range variants {
		registrations = append(registrations,
			fmt.Sprintf(`	Register%sVariant("%s", func(data []byte) (%sVariant, error) {
		var v %s
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, err
		}
		return v, nil
	})`, name, v.DiscValue, strings.ToLower(name[:1])+name[1:], v.TypeName))
	}

	initSrc := fmt.Sprintf("func init() {\n%s\n}", strings.Join(registrations, "\n"))
	decls, err := astgen.CreateDeclsFromSource(initSrc)
	if err != nil {
		return fmt.Errorf("failed to create init function: %w", err)
	}
	for _, d := range decls {
		g.builder.AddDecl(d)
	}

	// Generate As* accessor methods for the new variants
	g.generateAccessors(name, variants)

	return nil
}

func (g *Generator) collectVariants(parentName string, parentSchema *jsondef.Schema, allVariants []*jsondef.Schema, discriminatorField string) []OneOfVariant {
	var variants []OneOfVariant

	// Check if any variant name would collide with its ref type name.
	// If so, use ref types directly without creating wrapper structs.
	directRef := g.hasVariantNameConflict(parentName, allVariants, discriminatorField)

	for _, variant := range allVariants {
		if jsondef.IsNullType(variant) {
			continue
		}

		// Process variants with discriminator const
		if variant.Properties != nil {
			discProp, exists := variant.Properties[discriminatorField]
			if exists && discProp.Const != nil {
				value, ok := discProp.Const.StringValue()
				if !ok {
					continue
				}

				variantName := toTitleCase(parentName) + toTitleCase(value)
				fieldName := value

				if len(variant.AllOf) > 0 && variant.AllOf[0].Ref != "" {
					refTypeName := jsondef.ResolveRefGo(variant.AllOf[0].Ref)
					goRefTypeName := toTitleCase(refTypeName)

					if directRef {
						// Use ref type directly — no wrapper struct
						variants = append(variants, OneOfVariant{
							FieldName: fieldName,
							TypeName:  goRefTypeName,
							DiscValue: value,
							DirectRef: true,
						})
						continue
					}

					// Normal: create wrapper struct with anonymous embed
					mergedSchema := g.mergeAllOfWithProperties(variant)
					desc := ""
					if variant.Description != nil {
						desc = *variant.Description
					}

					var fields []astgen.StructField
					fields = append(fields, astgen.StructField{
						Type:  astgen.Ident(goRefTypeName),
						Embed: true,
					})
					fields = append(fields, astgen.StructField{
						Name: toTitleCase(discriminatorField),
						Type: astgen.TypeExpr("string"),
						Tag:  `json:"` + discriminatorField + `"`,
					})

					for propName, propSchema := range mergedSchema.Properties {
						if propName == discriminatorField {
							continue
						}
						def := jsondef.Classify(propName, propSchema)
						field, err := g.buildStructField(propName, propSchema, def, mergedSchema)
						if err != nil {
							continue
						}
						fields = append(fields, field)
					}

					// Include shared properties from the parent schema (e.g. id, name)
					// so the variant struct can marshal/unmarshal the full object.
					if parentSchema != nil && parentSchema.Properties != nil {
						for propName, propSchema := range parentSchema.Properties {
							if propName == discriminatorField {
								continue
							}
							// Skip if the variant already defines this property.
							if _, exists := mergedSchema.Properties[propName]; exists {
								continue
							}
							// Handle _meta as generic map (same as buildStructFields).
							if propName == "_meta" {
								fields = append(fields, astgen.StructField{
									Name: "Meta",
									Type: astgen.TypeExpr("map[string]any"),
									Tag:  `json:"_meta,omitempty"`,
								})
								continue
							}
							def := jsondef.Classify(propName, propSchema)
							field, err := g.buildStructField(propName, propSchema, def, parentSchema)
							if err != nil {
								continue
							}
							fields = append(fields, field)
						}
					}

					g.builder.AddDecl(astgen.StructDef(variantName, fields, desc))
				} else {
					// Generate the variant struct normally
					desc := ""
					if variant.Description != nil {
						desc = *variant.Description
					}
					if err := g.generateStruct(variantName, variant, desc); err != nil {
						continue
					}
				}

				variants = append(variants, OneOfVariant{
					FieldName: fieldName,
					TypeName:  variantName,
					DiscValue: value,
				})
				continue
			}
		}

		// Handle default variants (no discriminator const, pure allOf ref)
		if len(variant.AllOf) > 0 && variant.AllOf[0].Ref != "" {
			refTypeName := jsondef.ResolveRefGo(variant.AllOf[0].Ref)
			goRefTypeName := toTitleCase(refTypeName)
			// Derive short field name by stripping parent prefix
			fieldName := strings.TrimPrefix(goRefTypeName, toTitleCase(parentName))
			if fieldName == "" {
				fieldName = goRefTypeName
			}
			variants = append(variants, OneOfVariant{
				FieldName: fieldName,
				TypeName:  goRefTypeName,
				DiscValue: "", // default variant
				DirectRef: true,
			})
		}
	}

	return variants
}

// hasVariantNameConflict checks if creating wrapper types would cause naming conflicts
// with the referenced types (e.g., MCPServer + "http" → MCPServerHTTP == McpServerHttp).
func (g *Generator) hasVariantNameConflict(parentName string, allVariants []*jsondef.Schema, discriminatorField string) bool {
	for _, variant := range allVariants {
		if variant.Properties == nil {
			continue
		}
		discProp, exists := variant.Properties[discriminatorField]
		if !exists || discProp.Const == nil {
			continue
		}
		value, ok := discProp.Const.StringValue()
		if !ok {
			continue
		}
		if len(variant.AllOf) > 0 && variant.AllOf[0].Ref != "" {
			refTypeName := jsondef.ResolveRefGo(variant.AllOf[0].Ref)
			variantName := toTitleCase(parentName) + toTitleCase(value)
			if variantName == toTitleCase(refTypeName) {
				return true
			}
		}
	}
	return false
}

// mergeAllOfWithProperties merges allOf referenced schemas with inline properties.
func (g *Generator) mergeAllOfWithProperties(schema *jsondef.Schema) *jsondef.Schema {
	merged := &jsondef.Schema{
		Properties: make(map[string]*jsondef.Schema),
		Required:   schema.Required,
	}
	// Copy inline properties
	for k, v := range schema.Properties {
		merged.Properties[k] = v
	}
	return merged
}

func (g *Generator) generateMarshalJSON(typeName string, _ []OneOfVariant) {
	recv := strings.ToLower(typeName[:1])

	src := fmt.Sprintf(`func (%s %s) MarshalJSON() ([]byte, error) {
	if %s.variant == nil {
		return nil, fmt.Errorf("no variant is set for %s")
	}
	return json.Marshal(%s.variant)
}`, recv, typeName, recv, typeName, recv)

	fn, err := astgen.CreateMethodFromSource(src)
	if err != nil {
		fmt.Printf("Warning: failed to generate MarshalJSON for %s: %v\n", typeName, err)
		return
	}
	g.builder.AddDecl(fn)
}

// generateMarshalJSONWithDiscriminator generates MarshalJSON that injects the discriminator
// field into the JSON output. Used when variant types are referenced directly (no wrapper struct).
func (g *Generator) generateMarshalJSONWithDiscriminator(typeName string, discriminatorField string) {
	recv := strings.ToLower(typeName[:1])
	markerMethodName := "is" + typeName + "Variant"

	src := fmt.Sprintf(`func (%s %s) MarshalJSON() ([]byte, error) {
	if %s.variant == nil {
		return nil, fmt.Errorf("no variant is set for %s")
	}
	data, err := json.Marshal(%s.variant)
	if err != nil {
		return nil, err
	}
	disc := %s.variant.%s()
	if disc == "" {
		return data, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, err
	}
	obj["%s"], _ = json.Marshal(disc)
	return json.Marshal(obj)
}`, recv, typeName, recv, typeName, recv, recv, markerMethodName, discriminatorField)

	fn, err := astgen.CreateMethodFromSource(src)
	if err != nil {
		fmt.Printf("Warning: failed to generate MarshalJSON for %s: %v\n", typeName, err)
		return
	}
	g.builder.AddDecl(fn)
}

func (g *Generator) generateUnmarshalJSON(typeName string, variants []OneOfVariant, discriminatorField string) {
	recv := strings.ToLower(typeName[:1])
	discFieldName := toTitleCase(discriminatorField)

	var cases []string
	var defaultVariant *OneOfVariant
	for _, v := range variants {
		if v.DiscValue == "" {
			defaultVariant = &v
			continue
		}
		cases = append(cases, fmt.Sprintf(`	case "%s":
		var v %s
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		%s.variant = v
		return nil`, v.DiscValue, v.TypeName, recv))
	}

	// Add default case: try extension registry first, then fall back
	markerName := strings.ToLower(typeName[:1]) + typeName[1:] + "Variant"
	registryFn := "try" + typeName + "Extension"
	defaultCase := fmt.Sprintf(`	if fn, ok := %s(disc.%s); ok {
			v, err := fn(data)
			if err != nil {
				return err
			}
			%s.variant = v
			return nil
		}
		return fmt.Errorf("unknown discriminator value: %%s", disc.%s)`, registryFn, discFieldName, recv, discFieldName)
	if defaultVariant != nil {
		defaultCase = fmt.Sprintf(`	var v %s
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		%s.variant = v
		return nil`, defaultVariant.TypeName, recv)
	}

	// Generate the extension registry types and functions for this union
	if defaultVariant == nil {
		g.generateVariantRegistry(typeName, markerName, registryFn)
	}

	src := fmt.Sprintf(`func (%s *%s) UnmarshalJSON(data []byte) error {
	var disc struct {
		%s string `+"`"+`json:"%s"`+"`"+`
	}
	if err := json.Unmarshal(data, &disc); err != nil {
		return err
	}
	switch disc.%s {
%s
	default:
		%s
	}
}`, recv, typeName, discFieldName, discriminatorField, discFieldName, strings.Join(cases, "\n"), defaultCase)

	fn, err := astgen.CreateMethodFromSource(src)
	if err != nil {
		fmt.Printf("Warning: failed to generate UnmarshalJSON for %s: %v\n", typeName, err)
		return
	}
	g.builder.AddDecl(fn)
}

// generateVariantRegistry generates the extension registry for a discriminated union.
// This allows new variants to be registered at init time (e.g., from unstable schema).
func (g *Generator) generateVariantRegistry(typeName, markerName, lookupFn string) {
	registerFn := "Register" + typeName + "Variant"
	factoryType := typeName + "VariantFactory"
	registryVar := strings.ToLower(typeName[:1]) + typeName[1:] + "ExtVariants"

	src := fmt.Sprintf(`// %s is a factory function that deserializes a variant from JSON.
type %s func(data []byte) (%s, error)

var %s = map[string]%s{}

// %s registers a new variant factory for the %s union.
// Call this from init() to extend the union with additional variants.
func %s(discriminator string, factory %s) {
	%s[discriminator] = factory
}

func %s(discriminator string) (%s, bool) {
	fn, ok := %s[discriminator]
	return fn, ok
}`, factoryType, factoryType, markerName,
		registryVar, factoryType,
		registerFn, typeName,
		registerFn, factoryType,
		registryVar,
		lookupFn, factoryType,
		registryVar)

	decls, err := astgen.CreateDeclsFromSource(src)
	if err != nil {
		fmt.Printf("Warning: failed to generate variant registry for %s: %v\n", typeName, err)
		return
	}
	for _, d := range decls {
		g.builder.AddDecl(d)
	}
}

func (g *Generator) generateAccessors(typeName string, variants []OneOfVariant) {
	recv := strings.ToLower(typeName[:1])

	for _, v := range variants {
		accessorName := "As" + toTitleCase(v.FieldName)
		src := fmt.Sprintf(`func (%s *%s) %s() (%s, bool) {
	v, ok := %s.variant.(%s)
	return v, ok
}`, recv, typeName, accessorName, v.TypeName, recv, v.TypeName)

		fn, err := astgen.CreateMethodFromSource(src)
		if err != nil {
			fmt.Printf("Warning: failed to generate accessor %s for %s: %v\n", accessorName, typeName, err)
			continue
		}
		g.builder.AddDecl(fn)
	}
}

// generateConstants generates constants from metadata.
func (g *Generator) generateConstants() {
	if g.metadata == nil {
		return
	}

	// Load base metadata if excludeMetaFrom is set
	var baseMeta *Metadata
	if g.config.ExcludeMetaFrom != "" {
		var err error
		baseMeta, err = LoadMetadata(g.config.ExcludeMetaFrom)
		if err != nil {
			fmt.Printf("Warning: Failed to load base metadata for exclusion: %v\n", err)
		}
	}

	// Protocol version constant (skip if same as base)
	if baseMeta == nil || g.metadata.Version != baseMeta.Version {
		g.builder.AddRawDecl(astgen.SingleConstSource(
			"CurrentProtocolVersion", "int",
			fmt.Sprintf("%d", g.metadata.Version),
			"Current protocol version from metadata",
		))
	}

	// Determine suffix for unstable-only constants
	suffix := ""
	if baseMeta != nil {
		suffix = "Unstable"
	}

	// Agent methods (only new ones)
	agentMethods := diffMethods(g.metadata.AgentMethods, baseMeta.getAgentMethods())
	if len(agentMethods) > 0 {
		g.generateStructuredConstants("AgentMethods"+suffix, agentMethods, "Agent method names (unstable)")
	}

	// Client methods (only new ones)
	clientMethods := diffMethods(g.metadata.ClientMethods, baseMeta.getClientMethods())
	if len(clientMethods) > 0 {
		g.generateStructuredConstants("ClientMethods"+suffix, clientMethods, "Client method names (unstable)")
	}
}

// diffMethods returns entries in methods that are not in base (or differ).
func diffMethods(methods, base map[string]string) map[string]string {
	if base == nil {
		return methods
	}
	result := make(map[string]string)
	for k, v := range methods {
		if baseV, ok := base[k]; !ok || baseV != v {
			result[k] = v
		}
	}
	return result
}

func (g *Generator) generateStructuredConstants(varName string, methods map[string]string, comment string) {
	keys := make([]string, 0, len(methods))
	for key := range methods {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	entries := make([]astgen.StructVarEntry, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, astgen.StructVarEntry{
			Name:  formatFieldName(key),
			Type:  "string",
			Value: methods[key],
		})
	}

	g.builder.AddDecl(astgen.VarAnonymousStruct(varName, entries, comment))
}

func formatFieldName(key string) string {
	return toTitleCase(key)
}

func (g *Generator) isTypeIgnored(typeName string) bool {
	if slices.Contains(g.config.IgnoreTypes, typeName) {
		return true
	}
	if g.excludedDefNames[typeName] {
		return true
	}
	return false
}

func (g *Generator) addSkippedItem(name string) {
	g.skippedItems = append(g.skippedItems, name)
}

