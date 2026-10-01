package tsdef

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	ts "github.com/tree-sitter/go-tree-sitter"
	grammar "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

// Zod preserves builder order, including default/catch/optional wrappers.
// Nil Value is JavaScript undefined; JSON null is the literal bytes "null".
type Zod struct {
	Kind    string         `json:"kind"`
	Ref     string         `json:"ref,omitzero"`
	Inner   *Zod           `json:"inner,omitzero"`
	Members []*Zod         `json:"members,omitzero"`
	Fields  []ZodField     `json:"fields,omitzero"`
	Key     *Zod           `json:"key,omitzero"`
	Value   jsontext.Value `json:"value,omitzero"`
	Tag     string         `json:"tag,omitzero"`
	Tags    []string       `json:"tags,omitzero"`
	Pattern string         `json:"pattern,omitzero"`
	Offset  bool           `json:"offset,omitzero"`
	Loose   bool           `json:"loose,omitzero"` // object: keeps undeclared properties
}

// Children returns the rules nested directly in z.
func (z *Zod) Children() []*Zod {
	out := append([]*Zod{z.Inner, z.Key}, z.Members...)
	for _, f := range z.Fields {
		out = append(out, f.Schema)
	}
	return slices.DeleteFunc(out, func(c *Zod) bool { return c == nil })
}

type ZodField struct {
	Name   string `json:"name"`
	Schema *Zod   `json:"schema"`
}

// ParseZod recognizes the SDK's Zod subset without executing JavaScript.
func ParseZod(filename string, source []byte) (map[string]*Zod, error) {
	p := ts.NewParser()
	defer p.Close()
	if err := p.SetLanguage(ts.NewLanguage(grammar.LanguageTypescript())); err != nil {
		return nil, err
	}
	tree := p.Parse(source, nil)
	if tree == nil {
		return nil, fmt.Errorf("%s: parser returned no tree", filename)
	}
	defer tree.Close()
	r := reader{filename, source}
	root := tree.RootNode()
	if root.HasError() {
		return nil, r.fail(firstError(root), "invalid TypeScript syntax")
	}
	result := map[string]*Zod{}
	for _, n := range children(root) {
		if n.Kind() == "comment" || n.Kind() == "import_statement" {
			continue
		}
		if n.Kind() != "export_statement" {
			return nil, r.fail(n, "unsupported Zod declaration")
		}
		d := n.ChildByFieldName("declaration")
		if d == nil || d.Kind() != "lexical_declaration" {
			return nil, r.fail(n, "expected exported Zod constant")
		}
		for _, v := range children(d) {
			if v.Kind() != "variable_declarator" {
				return nil, r.fail(v, "unsupported Zod variable")
			}
			name := v.ChildByFieldName("name").Utf8Text(source)
			if !strings.HasPrefix(name, "z") {
				return nil, r.fail(v, "expected z-prefixed schema name")
			}
			if _, ok := result[name]; ok {
				return nil, r.fail(v, "duplicate Zod schema "+name)
			}
			schema, err := r.zod(v.ChildByFieldName("value"))
			if err != nil {
				return nil, err
			}
			result[name] = schema
		}
	}
	var check func(*Zod) error
	check = func(z *Zod) error {
		if z.Kind == "ref" && result[z.Ref] == nil {
			return fmt.Errorf("%s: unresolved Zod reference %s", filename, z.Ref)
		}
		for _, c := range z.Children() {
			if err := check(c); err != nil {
				return err
			}
		}
		return nil
	}
	for name, z := range result {
		if err := check(z); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
	}
	return result, nil
}
func (r reader) zod(n *ts.Node) (*Zod, error) {
	if n.Kind() == "identifier" {
		return &Zod{Kind: "ref", Ref: n.Utf8Text(r.source)}, nil
	}
	if n.Kind() != "call_expression" {
		return nil, r.fail(n, "unsupported Zod expression")
	}
	fn := n.ChildByFieldName("function")
	args := children(n.ChildByFieldName("arguments"))
	filtered := args[:0]
	for _, a := range args {
		if a.Kind() != "comment" {
			filtered = append(filtered, a)
		}
	}
	args = filtered
	text := fn.Utf8Text(r.source)
	method := text
	var inner *Zod
	if fn.Kind() == "member_expression" {
		object := fn.ChildByFieldName("object")
		method = fn.ChildByFieldName("property").Utf8Text(r.source)
		namespace := object.Utf8Text(r.source)
		if namespace != "z" && namespace != "z.iso" {
			var err error
			inner, err = r.zod(object)
			if err != nil {
				return nil, err
			}
		}
		if namespace == "z.iso" {
			if method != "datetime" {
				return nil, r.fail(fn, "unsupported Zod ISO builder")
			}
			method = "datetime"
		}
	}
	z := &Zod{Kind: method, Inner: inner}
	arity := func(min, max int) error {
		if len(args) < min || len(args) > max {
			return r.fail(n, fmt.Sprintf("%s expects %d..%d arguments", method, min, max))
		}
		return nil
	}
	one := func() error {
		if err := arity(1, 1); err != nil {
			return err
		}
		v, err := r.zod(args[0])
		z.Inner = v
		return err
	}
	if inner != nil {
		switch method {
		case "optional", "nullable", "nullish", "int":
			if err := arity(0, 0); err != nil {
				return nil, err
			}
		case "default":
			if err := arity(1, 1); err != nil {
				return nil, err
			}
			value, err := r.zodValue(args[0])
			if err != nil {
				return nil, err
			}
			z.Value = value
		case "min", "max", "gte", "lte":
			if err := arity(1, 2); err != nil {
				return nil, err
			}
			value, err := r.zodValue(args[0])
			if err != nil {
				return nil, err
			}
			var number float64
			if json.Unmarshal(value, &number) != nil || value.Kind() != '0' {
				return nil, r.fail(args[0], "expected numeric bound")
			}
			z.Value = value
			if len(args) == 2 {
				if err := r.zodOptions(args[1], map[string]bool{"error": true}); err != nil {
					return nil, err
				}
			}
		case "regex":
			if err := arity(1, 1); err != nil {
				return nil, err
			}
			raw := args[0].Utf8Text(r.source)
			if args[0].Kind() != "regex" || !strings.HasSuffix(raw, "/") {
				return nil, r.fail(args[0], "only unflagged regular expression literals are supported")
			}
			z.Pattern = strings.TrimSuffix(strings.TrimPrefix(raw, "/"), "/")
			if _, err := regexp.Compile(z.Pattern); err != nil {
				return nil, r.fail(args[0], "regular expression is not supported by Go")
			}
		case "and":
			if err := arity(1, 1); err != nil {
				return nil, err
			}
			other, err := r.zod(args[0])
			if err != nil {
				return nil, err
			}
			z.Kind = "intersection"
			z.Inner = nil
			z.Members = []*Zod{inner, other}
		default:
			return nil, r.fail(fn, "unsupported Zod method "+method)
		}
		return z, nil
	}
	if fn.Kind() == "identifier" {
		switch method {
		case "vecSkipError":
			z.Kind = "skipArray"
			if err := one(); err != nil {
				return nil, err
			}
		case "defaultOnError", "requiredDefaultOnError":
			if err := arity(2, 2); err != nil {
				return nil, err
			}
			var err error
			z.Inner, err = r.zod(args[0])
			if err != nil {
				return nil, err
			}
			if args[1].Kind() != "arrow_function" {
				return nil, r.fail(args[1], "expected literal fallback callback")
			}
			z.Value, err = r.zodValue(args[1].ChildByFieldName("body"))
			if err != nil {
				return nil, err
			}
			z.Kind = "catch"
			if method == "requiredDefaultOnError" {
				z.Kind = "requiredCatch"
			}
		case "excludeKnownTags", "preserveCustomPayload":
			if err := arity(3, 3); err != nil {
				return nil, err
			}
			var err error
			z.Inner, err = r.zod(args[0])
			if err != nil {
				return nil, err
			}
			key, err := r.zodValue(args[1])
			if err != nil {
				return nil, err
			}
			if json.Unmarshal(key, &z.Tag) != nil || key.Kind() != '"' {
				return nil, r.fail(args[1], "expected tag name")
			}
			tags, err := r.zodValue(args[2])
			if err != nil {
				return nil, err
			}
			if tags.Kind() != '[' || json.Unmarshal(tags, &z.Tags) != nil {
				return nil, r.fail(args[2], "expected known string tags")
			}
			z.Kind = "excludeTags"
			if method == "preserveCustomPayload" {
				z.Kind = "preserve"
			}
		default:
			return nil, r.fail(fn, "unsupported Zod helper "+method)
		}
		return z, nil
	}
	switch method {
	case "string", "number", "boolean", "unknown", "any", "null", "never", "int", "url":
		if err := arity(0, 0); err != nil {
			return nil, err
		}
	case "datetime":
		if err := arity(0, 1); err != nil {
			return nil, err
		}
		if len(args) == 1 {
			if err := r.zodOptions(args[0], map[string]bool{"offset": true}); err != nil {
				return nil, err
			}
			raw, err := r.zodValue(args[0])
			if err != nil {
				return nil, err
			}
			var options struct {
				Offset bool `json:"offset"`
			}
			if err = json.Unmarshal(raw, &options); err != nil {
				return nil, r.fail(args[0], "invalid datetime options")
			}
			z.Offset = options.Offset
		}
	case "literal":
		if err := arity(1, 1); err != nil {
			return nil, err
		}
		v, err := r.zodValue(args[0])
		if err != nil {
			return nil, err
		}
		if v.Kind() != '"' && v.Kind() != '0' && v.Kind() != 't' && v.Kind() != 'f' && v.Kind() != 'n' {
			return nil, r.fail(args[0], "only scalar Zod literals are supported")
		}
		z.Value = v
	case "array":
		if err := one(); err != nil {
			return nil, err
		}
	case "record":
		if err := arity(2, 2); err != nil {
			return nil, err
		}
		var err error
		z.Key, err = r.zod(args[0])
		if err != nil {
			return nil, err
		}
		z.Inner, err = r.zod(args[1])
		if err != nil {
			return nil, err
		}
	case "object", "looseObject":
		if err := arity(1, 1); err != nil {
			return nil, err
		}
		if args[0].Kind() != "object" {
			return nil, r.fail(args[0], "expected literal object shape")
		}
		z.Kind, z.Loose = "object", method == "looseObject"
		seen := map[string]bool{}
		for _, pair := range children(args[0]) {
			if pair.Kind() == "comment" {
				continue
			}
			if pair.Kind() != "pair" {
				return nil, r.fail(pair, "unsupported Zod object member")
			}
			key := pair.ChildByFieldName("key")
			name := key.Utf8Text(r.source)
			if key.Kind() == "string" {
				var err error
				name, err = strconv.Unquote(name)
				if err != nil {
					return nil, r.fail(key, "unsupported property string")
				}
			}
			if seen[name] {
				return nil, r.fail(key, "duplicate Zod property")
			}
			seen[name] = true
			v, err := r.zod(pair.ChildByFieldName("value"))
			if err != nil {
				return nil, err
			}
			z.Fields = append(z.Fields, ZodField{Name: name, Schema: v})
		}
	case "union":
		if err := arity(1, 1); err != nil {
			return nil, err
		}
		if args[0].Kind() != "array" {
			return nil, r.fail(args[0], "expected literal union array")
		}
		for _, a := range children(args[0]) {
			if a.Kind() == "comment" {
				continue
			}
			m, err := r.zod(a)
			if err != nil {
				return nil, err
			}
			z.Members = append(z.Members, m)
		}
		if len(z.Members) == 0 {
			return nil, r.fail(args[0], "empty union")
		}
	case "intersection":
		if err := arity(2, 2); err != nil {
			return nil, err
		}
		for _, a := range args {
			m, err := r.zod(a)
			if err != nil {
				return nil, err
			}
			z.Members = append(z.Members, m)
		}
	default:
		return nil, r.fail(fn, "unsupported Zod builder "+text)
	}
	return z, nil
}
func (r reader) zodOptions(n *ts.Node, allowed map[string]bool) error {
	if n.Kind() != "object" {
		return r.fail(n, "expected literal options")
	}
	for _, p := range children(n) {
		if p.Kind() == "comment" {
			continue
		}
		if p.Kind() != "pair" {
			return r.fail(p, "unsupported options member")
		}
		key := strings.Trim(p.ChildByFieldName("key").Utf8Text(r.source), "\"")
		if !allowed[key] {
			return r.fail(p, "unsupported Zod option "+key)
		}
	}
	return nil
}
func (r reader) zodValue(n *ts.Node) (jsontext.Value, error) {
	switch n.Kind() {
	case "parenthesized_expression", "as_expression":
		return r.zodValue(n.NamedChild(0))
	case "undefined":
		return nil, nil
	case "identifier":
		if n.Utf8Text(r.source) == "undefined" {
			return nil, nil
		}
	case "string":
		value, err := strconv.Unquote(n.Utf8Text(r.source))
		if err != nil {
			return nil, r.fail(n, "unsupported string literal")
		}
		return json.Marshal(value)
	case "number", "unary_expression", "true", "false", "null":
		raw := jsontext.Value(n.Utf8Text(r.source))
		if raw.IsValid() {
			return raw.Clone(), nil
		}
	case "array":
		values := []jsontext.Value{}
		for _, c := range children(n) {
			if c.Kind() == "comment" {
				continue
			}
			v, err := r.zodValue(c)
			if err != nil {
				return nil, err
			}
			if v == nil {
				return nil, r.fail(c, "undefined array literal is unsupported")
			}
			values = append(values, v)
		}
		return json.Marshal(values)
	case "object":
		values := map[string]jsontext.Value{}
		for _, p := range children(n) {
			if p.Kind() == "comment" {
				continue
			}
			if p.Kind() != "pair" {
				return nil, r.fail(p, "unsupported literal object member")
			}
			key := p.ChildByFieldName("key").Utf8Text(r.source)
			if strings.HasPrefix(key, "\"") {
				key, _ = strconv.Unquote(key)
			}
			v, err := r.zodValue(p.ChildByFieldName("value"))
			if err != nil {
				return nil, err
			}
			if v == nil {
				return nil, r.fail(p, "undefined object literal is unsupported")
			}
			values[key] = v
		}
		return json.Marshal(values, json.Deterministic(true))
	}
	return nil, r.fail(n, "expected a static JSON value or undefined")
}
