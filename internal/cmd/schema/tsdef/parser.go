package tsdef

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	ts "github.com/tree-sitter/go-tree-sitter"
	grammar "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

// ParseDir reads declarations and protocol constants from a SDK schema directory.
// Zod and guard implementation files are not type declaration inputs.
func ParseDir(dir string) (*Schema, error) {
	result := &Schema{}
	for _, name := range []string{"types.gen.ts", "index.ts"} {
		path := filepath.Join(dir, name)
		source, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		part, err := Parse(path, source)
		if err != nil {
			return nil, err
		}
		result.Types = append(result.Types, part.Types...)
		result.Constants = append(result.Constants, part.Constants...)
	}

	path := filepath.Join(dir, "zod.gen.ts")
	source, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	result.Validators, err = ParseZod(path, source)
	if err != nil {
		return nil, err
	}
	if err := ApplyNumericHints(result); err != nil {
		return nil, err
	}
	return result, nil
}

// Parse extracts exported type aliases and literal constants, ignoring imports,
// re-exports and private implementation declarations.
func Parse(filename string, source []byte) (*Schema, error) {
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
	result := &Schema{}
	comment := ""
	for _, n := range children(root) {
		if n.Kind() == "comment" {
			comment = doc(n.Utf8Text(source))
			continue
		}
		if n.Kind() == "export_statement" {
			decl := n.ChildByFieldName("declaration")
			if decl != nil {
				switch decl.Kind() {
				case "type_alias_declaration":
					typ, err := r.typ(decl.ChildByFieldName("value"))
					if err != nil {
						return nil, err
					}
					result.Types = append(result.Types, Definition{Name: decl.ChildByFieldName("name").Utf8Text(source), Comment: comment, Type: typ})
				case "lexical_declaration":
					for _, v := range children(decl) {
						if v.Kind() != "variable_declarator" {
							return nil, r.fail(v, "unsupported constant declaration")
						}
						c, err := r.constant(v.ChildByFieldName("value"))
						if err != nil {
							return nil, err
						}
						c.Name = v.ChildByFieldName("name").Utf8Text(source)
						result.Constants = append(result.Constants, c)
					}
				default:
					return nil, r.fail(decl, "unsupported exported declaration")
				}
			}
		}
		comment = ""
	}
	return result, nil
}

type reader struct {
	filename string
	source   []byte
}

func (r reader) fail(n *ts.Node, message string) error {
	pos := n.StartPosition()
	return fmt.Errorf("%s:%d:%d: %s (%s)", r.filename, pos.Row+1, pos.Column+1, message, n.Kind())
}
func firstError(n *ts.Node) *ts.Node {
	for _, c := range children(n) {
		if c.HasError() || c.IsMissing() {
			return firstError(c)
		}
	}
	return n
}
func children(n *ts.Node) []*ts.Node {
	var result []*ts.Node
	for i := uint(0); i < n.NamedChildCount(); i++ {
		result = append(result, n.NamedChild(i))
	}
	return result
}
func doc(s string) string {
	if !strings.HasPrefix(s, "/**") {
		return ""
	}
	s = strings.TrimSuffix(strings.TrimPrefix(s, "/**"), "*/")
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "*"))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// primitives maps TypeScript's predefined types to their kinds.
var primitives = map[string]Kind{
	"string": KindString, "number": KindNumber, "boolean": KindBoolean,
	"unknown": KindUnknown, "never": KindNever, "any": KindAny,
}

func (r reader) typ(n *ts.Node) (*Type, error) {
	t := &Type{}
	switch n.Kind() {
	case "type_annotation", "parenthesized_type":
		return r.typ(n.NamedChild(0))
	case "predefined_type":
		kind, ok := primitives[n.Utf8Text(r.source)]
		if !ok {
			return nil, r.fail(n, "unsupported primitive")
		}
		t.Kind = kind
	case "type_identifier":
		t.Kind = KindRef
		t.Name = n.Utf8Text(r.source)
	case "literal_type":
		child := n.NamedChild(0)
		t.Kind = KindLiteral
		t.Literal = child.Utf8Text(r.source)
		switch child.Kind() {
		case "null":
			t.Kind = KindNull
		case "string":
			value, err := strconv.Unquote(t.Literal)
			if err != nil {
				return nil, r.fail(child, "unsupported string literal")
			}
			t.Literal = strconv.Quote(value)
		case "number", "true", "false", "unary_expression":
		default:
			return nil, r.fail(n, "unsupported literal")
		}
	case "union_type", "intersection_type":
		t.Kind = KindUnion
		if n.Kind() == "intersection_type" {
			t.Kind = KindIntersection
		}
		for _, c := range children(n) {
			if c.Kind() == "comment" {
				continue
			}
			item, err := r.typ(c)
			if err != nil {
				return nil, err
			}
			if item.Kind == t.Kind {
				t.Members = append(t.Members, item.Members...)
			} else {
				t.Members = append(t.Members, item)
			}
		}
	case "array_type":
		t.Kind = KindArray
		elem, err := r.typ(n.NamedChild(0))
		if err != nil {
			return nil, err
		}
		t.Element = elem
	case "generic_type":
		name := n.ChildByFieldName("name").Utf8Text(r.source)
		args := n.ChildByFieldName("type_arguments")
		if name != "Array" || args.NamedChildCount() != 1 {
			return nil, r.fail(n, "unsupported generic "+name)
		}
		elem, err := r.typ(args.NamedChild(0))
		if err != nil {
			return nil, err
		}
		t.Kind = KindArray
		t.Element = elem
	case "object_type":
		t.Kind = KindObject
		comment := ""
		for _, c := range children(n) {
			if c.Kind() == "comment" {
				comment = doc(c.Utf8Text(r.source))
				continue
			}
			switch c.Kind() {
			case "property_signature":
				nameNode := c.ChildByFieldName("name")
				name := nameNode.Utf8Text(r.source)
				if nameNode.Kind() == "string" {
					var err error
					name, err = strconv.Unquote(name)
					if err != nil {
						return nil, r.fail(nameNode, "unsupported property name")
					}
				}
				annotation := c.ChildByFieldName("type")
				if annotation == nil {
					return nil, r.fail(c, "property requires an explicit type")
				}
				value, err := r.typ(annotation)
				if err != nil {
					return nil, err
				}
				optional := false
				for i := uint(0); i < c.ChildCount(); i++ {
					if c.Child(i).Kind() == "?" {
						optional = true
					}
				}
				t.Fields = append(t.Fields, Field{Name: name, Comment: comment, Optional: optional, Type: value})
			case "index_signature":
				if c.ChildByFieldName("index_type") == nil || c.ChildByFieldName("index_type").Utf8Text(r.source) != "string" {
					return nil, r.fail(c, "only string index signatures are supported")
				}
				elem, err := r.typ(c.ChildByFieldName("type"))
				if err != nil {
					return nil, err
				}
				t.Element = elem
			default:
				return nil, r.fail(c, "unsupported object member")
			}
			comment = ""
		}
	default:
		return nil, r.fail(n, "unsupported schema type")
	}
	return t, nil
}
func (r reader) constant(n *ts.Node) (Constant, error) {
	if n.Kind() == "as_expression" {
		return r.constant(n.NamedChild(0))
	}
	c := Constant{}
	switch n.Kind() {
	case "string":
		value, err := strconv.Unquote(n.Utf8Text(r.source))
		if err != nil {
			return c, r.fail(n, "unsupported string constant")
		}
		c.Value = strconv.Quote(value)
	case "number", "true", "false":
		c.Value = n.Utf8Text(r.source)
	case "object":
		for _, pair := range children(n) {
			if pair.Kind() == "comment" {
				continue
			}
			if pair.Kind() != "pair" {
				return c, r.fail(pair, "unsupported constant member")
			}
			member, err := r.constant(pair.ChildByFieldName("value"))
			if err != nil {
				return c, err
			}
			member.Name = pair.ChildByFieldName("key").Utf8Text(r.source)
			c.Members = append(c.Members, member)
		}
	default:
		return c, r.fail(n, "unsupported constant value")
	}
	return c, nil
}
