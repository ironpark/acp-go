package tsgen

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/ironpark/acp-go/internal/cmd/schema/tsdef"
)

// ZodRuntime is the import path of the shared rule evaluator.
const ZodRuntime = "github.com/ironpark/acp-go/schema/internal/zod"

// UnionRuntime is the import path of the shared raw-union alternative matcher.
const UnionRuntime = "github.com/ironpark/acp-go/schema/internal/union"

// MetaRuntime is the import path of the shared _meta map type.
const MetaRuntime = "github.com/ironpark/acp-go/schema/meta"

// zodKinds maps parsed builder names to zod.Kind constant names. Unknown
// builders fail generation instead of producing an unsupported rule.
var zodKinds = map[string]string{
	"ref": "Ref", "optional": "Optional", "nullish": "Nullish", "nullable": "Nullable",
	"default": "Default", "catch": "Catch", "requiredCatch": "RequiredCatch",
	"unknown": "Unknown", "any": "Any", "never": "Never",
	"union": "Union", "intersection": "Intersection",
	"excludeTags": "ExcludeTags", "preserve": "Preserve", "openTags": "OpenTags",
	"min": "Min", "max": "Max", "gte": "Gte", "lte": "Lte", "regex": "Regex", "int": "Int",
	"null": "Null", "string": "String", "boolean": "Boolean", "number": "Number", "literal": "Literal",
	"url": "URL", "datetime": "DateTime",
	"array": "Array", "skipArray": "SkipArray", "object": "Object", "record": "Record",
}

// zod writes the rule registry and per-type Decode/Validate functions.
func (g *generator) zod(schema *tsdef.Schema) error {
	if len(schema.Validators) == 0 {
		return nil
	}
	type entry struct {
		key  string
		rule *tsdef.Zod
	}
	var entries []entry
	for _, k := range slices.Sorted(maps.Keys(schema.Validators)) {
		z := schema.Validators[k]
		if open, ok := g.openTags[Name(strings.TrimPrefix(k, "z"))]; ok {
			z = &tsdef.Zod{Kind: "openTags", Tag: open.tag, Tags: open.values, Inner: z}
		}
		entries = append(entries, entry{k, z})
	}

	defs := append([]tsdef.Definition(nil), schema.Types...)
	slices.SortFunc(defs, func(a, b tsdef.Definition) int { return strings.Compare(a.Name, b.Name) })
	var types, unmarshalers []string
	register := func(goName, key string) {
		types = append(types, fmt.Sprintf("reflect.TypeFor[%s](): %q", goName, key))
		unmarshalers = append(unmarshalers, fmt.Sprintf("zod.Unmarshaler[%s](zodSchemas, %q)", goName, key))
	}
	for _, d := range defs {
		key := "z" + d.Name
		if schema.Validators[key] == nil {
			return fmt.Errorf("missing Zod schema for %s", d.Name)
		}
		name := Name(d.Name)
		if g.aliases[name] {
			// Aliases share their reflect.Type with the underlying type, so a
			// rule registered for them would apply to every value of that type.
			continue
		}
		if g.isAbsorbed(name) {
			continue // registered below under the rule its variant carries
		}
		register(name, key)
	}
	for _, goName := range slices.Sorted(maps.Keys(g.absorbed)) {
		v := g.absorbed[goName]
		if v.variant == "" {
			continue // the union was never emitted
		}
		// The variant writes its tag, so its rule is the absorbed type's
		// own rule intersected with that tag.
		key := fmt.Sprintf("z%s&%s=%s", v.base, v.tag, v.value)
		entries = append(entries, entry{key, &tsdef.Zod{Kind: "intersection", Members: []*tsdef.Zod{
			{Kind: "ref", Ref: "z" + v.base},
			{Kind: "object", Fields: []tsdef.ZodField{{Name: v.tag, Schema: &tsdef.Zod{Kind: "literal", Value: jsontext.Value(v.value)}}}},
		}}})
		register(v.variant, key)
	}

	r := &zodRenderer{keys: map[*tsdef.Zod]string{}, counts: map[string]int{}, first: map[string]*tsdef.Zod{}}
	for _, e := range entries {
		if err := r.count(e.rule); err != nil {
			return fmt.Errorf("%s: %w", e.key, err)
		}
	}
	r.share()
	var registry []string
	for _, e := range entries {
		registry = append(registry, fmt.Sprintf("%s: %s,", strconv.Quote(e.key), r.literal(e.rule)))
	}
	if shared := r.vars(); shared != "" {
		g.write("// Rule subtrees that repeat across the schemas, written and allocated once.\nvar (\n%s)\n\n", shared)
	}
	g.write("// zodSchemas holds the SDK Zod rules; one rule tree per schema name, linked\n// once for evaluation.\nvar zodSchemas = zod.Link(zod.Registry{\n%s\n})\n\n", strings.Join(registry, "\n"))
	g.write("// zodTypes maps generated Go types to their Zod rule. Type aliases are not\n// listed; they share a reflect.Type with their underlying type.\nvar zodTypes = map[reflect.Type]string{\n%s,\n}\n\n", strings.Join(types, ",\n"))
	g.write("// Validated returns the json.Options that apply the SDK Zod validation, default\n// and recovery rules to every generated type encountered while unmarshaling:\n//\n//\tjson.Unmarshal(data, &v, schema.Validated())\n//\n// Type aliases are decoded as their underlying type.\nfunc Validated() json.Options { return validated }\n\nvar validated = json.WithUnmarshalers(json.JoinUnmarshalers(\n%s,\n))\n\n", strings.Join(unmarshalers, ",\n"))
	g.out.WriteString(`// zodRule returns the Zod rule registered for T.
func zodRule[T any]() (string, error) {
	name, ok := zodTypes[reflect.TypeFor[T]()]
	if !ok {
		var zero T
		return "", fmt.Errorf("no Zod rule for %T", zero)
	}
	return name, nil
}

// Decode applies the supported SDK Zod validation, defaults and recovery rules
// for T, then decodes the normalized value. T must be a generated non-alias type.
func Decode[T any](raw []byte) (T, error) {
	name, err := zodRule[T]()
	if err != nil {
		var zero T
		return zero, err
	}
	return zod.Decode[T](zodSchemas, name, raw)
}

// Validate reports whether the supported SDK Zod parser accepts raw as a T.
// Recovery and defaults are applied; use Decode to obtain the normalized value.
// Unlike the SDK, unknown tags of tagged unions are accepted: they decode into
// the union's Unknown variant.
func Validate[T any](raw []byte) error {
	name, err := zodRule[T]()
	if err != nil {
		return err
	}
	_, err = zodSchemas.Normalize(name, raw)
	return err
}
`)
	return nil
}

// minShared is the shortest rendered subtree worth a shared variable; a
// shorter one reads better written out.
const minShared = 40

// zodRenderer renders rule trees as Go composite literals. A subtree that
// repeats is written once as a variable, named from its contents so that the
// name survives changes to unrelated schemas, and referenced by that name
// everywhere it occurs.
type zodRenderer struct {
	keys   map[*tsdef.Zod]string // each node's single-line form, its identity
	counts map[string]int        // how often each form occurs
	first  map[string]*tsdef.Zod // a node with each form, to render it from
	names  map[string]string     // the variable name of each shared form
}

// count records z and its subtrees, failing on a rule the runtime lacks, so
// rendering them afterwards cannot fail.
func (r *zodRenderer) count(z *tsdef.Zod) error {
	if _, ok := zodKinds[z.Kind]; !ok {
		return fmt.Errorf("unsupported Zod rule %q", z.Kind)
	}
	for _, c := range z.Children() {
		if err := r.count(c); err != nil {
			return err
		}
	}
	key := r.key(z)
	r.counts[key]++
	if r.first[key] == nil {
		r.first[key] = z
	}
	return nil
}

func (r *zodRenderer) key(z *tsdef.Zod) string {
	if key, ok := r.keys[z]; ok {
		return key
	}
	key := renderZod(z, r.key, false)
	r.keys[z] = key
	return key
}

// share names every form that occurs more than once and is long enough.
func (r *zodRenderer) share() {
	r.names = map[string]string{}
	used := map[string]bool{}
	// Sorted, so a hash prefix two forms share lengthens the same one's name
	// every time.
	for _, key := range slices.Sorted(maps.Keys(r.counts)) {
		if r.counts[key] < 2 || len(key) < minShared {
			continue
		}
		name := sharedName(r.first[key], key, 4)
		for n := 5; used[name]; n++ {
			name = sharedName(r.first[key], key, n)
		}
		used[name] = true
		r.names[key] = name
	}
}

// sharedName names a shared rule for what it is: a reference after the
// schema it names, any other rule after the kinds of its first few nested
// rules and a hash of its form, n bytes long.
func sharedName(z *tsdef.Zod, key string, n int) string {
	if z.Kind == "ref" {
		name := "zodRef" + strings.TrimPrefix(z.Ref, "z")
		if n > 4 {
			sum := sha256.Sum256([]byte(key))
			name += "_" + hex.EncodeToString(sum[:n])
		}
		return name
	}
	var b strings.Builder
	b.WriteString("zod")
	for c, depth := z, 0; c != nil && depth < 3; c, depth = c.Inner, depth+1 {
		b.WriteString(zodKinds[c.Kind])
	}
	sum := sha256.Sum256([]byte(key))
	b.WriteString("_" + hex.EncodeToString(sum[:n]))
	return b.String()
}

// literal renders z, as its variable name when it is shared.
func (r *zodRenderer) literal(z *tsdef.Zod) string {
	if name, ok := r.names[r.keys[z]]; ok {
		return name
	}
	return renderZod(z, r.literal, true)
}

// vars renders the shared variables, sorted by name.
func (r *zodRenderer) vars() string {
	var b strings.Builder
	for _, key := range slices.SortedFunc(maps.Keys(r.names), func(a, b string) int { return strings.Compare(r.names[a], r.names[b]) }) {
		fmt.Fprintf(&b, "%s = %s\n", r.names[key], renderZod(r.first[key], r.literal, true))
	}
	return b.String()
}

// renderZod renders one rule as a Go composite literal, its nested rules
// through child. A multiline rule puts each of two or more members or fields
// on its own line; the single-line form identifies a subtree.
func renderZod(z *tsdef.Zod, child func(*tsdef.Zod) string, multiline bool) string {
	list := func(items []string) string {
		if !multiline || len(items) < 2 {
			return strings.Join(items, ", ")
		}
		return "\n" + strings.Join(items, ",\n") + ",\n"
	}
	parts := []string{"Kind: zod.Kind" + zodKinds[z.Kind]}
	if z.Ref != "" {
		parts = append(parts, "Ref: "+strconv.Quote(z.Ref))
	}
	if z.Inner != nil {
		parts = append(parts, "Inner: "+child(z.Inner))
	}
	if len(z.Members) > 0 {
		var members []string
		for _, m := range z.Members {
			members = append(members, child(m))
		}
		parts = append(parts, "Members: []*zod.Rule{"+list(members)+"}")
	}
	if len(z.Fields) > 0 {
		var fields []string
		for _, f := range z.Fields {
			fields = append(fields, fmt.Sprintf("{Name: %s, Schema: %s}", strconv.Quote(f.Name), child(f.Schema)))
		}
		parts = append(parts, "Fields: []zod.Field{"+list(fields)+"}")
	}
	if z.Key != nil {
		parts = append(parts, "Key: "+child(z.Key))
	}
	if z.Value != nil {
		parts = append(parts, "Value: jsontext.Value("+goString(string(z.Value))+")")
	}
	if z.Tag != "" {
		parts = append(parts, "Tag: "+strconv.Quote(z.Tag))
	}
	if len(z.Tags) > 0 {
		var tags []string
		for _, t := range z.Tags {
			tags = append(tags, strconv.Quote(t))
		}
		parts = append(parts, "Tags: []string{"+strings.Join(tags, ", ")+"}")
	}
	if z.Pattern != "" {
		parts = append(parts, "Regexp: regexp.MustCompile("+goString(z.Pattern)+")")
	}
	if z.Offset {
		parts = append(parts, "Offset: true")
	}
	if z.Loose {
		parts = append(parts, "Loose: true")
	}
	return "&zod.Rule{" + strings.Join(parts, ", ") + "}"
}

// goString prefers raw string literals for readability when the value allows it.
func goString(s string) string {
	if !strings.ContainsAny(s, "`\n") {
		return "`" + s + "`"
	}
	return strconv.Quote(s)
}
