package zod

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// SDK datetimes use RFC 3339 with seconds and optional arbitrary fractions.
var datetimePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]([01]\d|2[0-3]):[0-5]\d)$`)

// outcome is what a rule made of a value: the value, or nil when it became
// absent, and the object properties the rule evaluated, which a custom
// payload leaves to the variant that declared them.
type outcome struct {
	n *node
	// object and more are the object rules evaluated, record any record:
	// together they say which properties were evaluated. object holds the
	// common single rule without allocating a slice.
	object *Rule
	more   []*Rule
	record bool
}

// objects returns the object rules evaluated.
func (o outcome) objects() []*Rule {
	if o.object == nil {
		return o.more
	}
	return append([]*Rule{o.object}, o.more...)
}

// evaluated reports whether the rules that produced o evaluated the property
// name of their input.
func (o outcome) evaluated(name string) bool {
	return o.record || slices.ContainsFunc(o.objects(), func(s *Rule) bool {
		return s.Loose || s.declares(name)
	})
}

// ruleError is a value a rule rejected. It is formatted only when read: an
// optional property's rule and a union's alternatives make and discard many.
type ruleError struct {
	path    *jsonPath
	message string
	value   []byte // the expected value, when the message names one
}

func (e *ruleError) Error() string {
	if e.value != nil {
		return fmt.Sprintf("%s: %s %s", e.path, e.message, e.value)
	}
	return fmt.Sprintf("%s: %s", e.path, e.message)
}

// jsonPath is where in the input a rule applies, built on the stack as
// evaluation descends and copied to the heap only for an error. The root is
// nil.
type jsonPath struct {
	parent  *jsonPath
	name    string
	index   int
	element bool // index, not name, locates the value
}

func (p *jsonPath) String() string {
	if p == nil {
		return "$"
	}
	if p.element {
		return fmt.Sprintf("%s[%d]", p.parent.String(), p.index)
	}
	return fmt.Sprintf("%s[%q]", p.parent.String(), p.name)
}

// clone copies the path out of the stack frames it was built in.
func (p *jsonPath) clone() *jsonPath {
	if p == nil {
		return nil
	}
	c := *p
	c.parent = p.parent.clone()
	return &c
}

// apply evaluates s on n, the value at path, or nil for an absent one.
func (r Registry) apply(s *Rule, n *node, path *jsonPath, depth int) (outcome, error) {
	if depth > 512 {
		return outcome{}, fmt.Errorf("%s: schema nesting limit exceeded", path.String())
	}
	pass := func() (outcome, error) { return outcome{n: n}, nil }
	fail := func(message string) (outcome, error) {
		return outcome{}, &ruleError{path: path.clone(), message: message}
	}
	inner := func() (outcome, error) { return r.apply(s.Inner, n, path, depth+1) }
	switch s.Kind {
	case KindRef:
		return r.apply(s.target, n, path, depth+1)
	case KindOptional, KindNullish:
		if s.Kind == KindNullish && kindOf(n) == 'n' {
			return pass()
		}
		if n != nil {
			return inner()
		}
		if s.absentReady {
			if s.absentOK {
				return s.absent, nil
			}
			return pass()
		}
		if result, err := inner(); err == nil {
			return result, nil
		}
		return pass()
	case KindNullable:
		if kindOf(n) == 'n' {
			return pass()
		}
		return inner()
	case KindDefault:
		if n == nil {
			return outcome{n: s.value}, nil
		}
		return inner()
	case KindCatch, KindRequiredCatch:
		if n == nil && s.Kind == KindRequiredCatch {
			return fail("required value is missing")
		}
		if n == nil && s.absentReady {
			if s.absentOK {
				return s.absent, nil
			}
			return outcome{n: s.value}, nil
		}
		value, err := inner()
		if err != nil {
			return outcome{n: s.value}, nil
		}
		return value, nil
	case KindNever:
		return fail("value is not permitted")
	case KindUnion:
		if s.cases != nil {
			tag, found := tagOf(n, s.tag)
			if member := s.cases[string(tag)]; found && member != nil {
				return r.apply(member, n, path, depth+1)
			}
			if s.fallback != nil {
				return r.apply(s.fallback, n, path, depth+1)
			}
			return fail(fmt.Sprintf("no union alternative matched: %q is not a known %s", tag, s.tag))
		}
		var problems []error
		for _, m := range s.Members {
			result, err := r.apply(m, n, path, depth+1)
			if err == nil {
				return result, nil
			}
			problems = append(problems, err)
		}
		return outcome{}, fmt.Errorf("%s: no union alternative matched: %w", path.String(), errors.Join(problems...))
	case KindIntersection:
		if s.flat != nil {
			return r.apply(s.flat, n, path, depth+1)
		}
		result := outcome{}
		for i, m := range s.Members {
			next, err := r.apply(m, n, path, depth+1)
			if err != nil {
				return result, err
			}
			if i == 0 {
				result = next
				continue
			}
			merged, err := merge(result.n, next.n, path)
			if err != nil {
				return outcome{}, err
			}
			result.n = merged
			result.record = result.record || next.record
			result.more = append(result.more, next.objects()...)
		}
		return result, nil
	case KindOpenTags:
		// A Go-side extension: objects whose Tag is a string outside Tags skip
		// the SDK rule and decode into the union's Unknown variant.
		if tag, found := tagOf(n, s.Tag); found && !hasTag(s.Tags, tag) {
			return pass()
		}
		return inner()
	case KindExcludeTags:
		result, err := inner()
		if err != nil {
			return result, err
		}
		if tag, found := tagOf(result.n, s.Tag); found && hasTag(s.Tags, tag) {
			return fail(fmt.Sprintf("%s %q is reserved by a known variant", s.Tag, tag))
		}
		return result, nil
	case KindPreserve:
		result, err := inner()
		if err != nil {
			return result, err
		}
		if tag, found := tagOf(n, s.Tag); !found || hasTag(s.Tags, tag) {
			return result, nil
		}
		if kindOf(result.n) != '{' {
			return fail("custom payload must be an object")
		}
		// Restore the properties no variant evaluated.
		var members []member // nil until one is restored
		for _, m := range n.members {
			if m.name == "__proto__" || result.evaluated(m.name) || result.n.get(m.name) != nil {
				continue
			}
			if members == nil {
				members = slices.Clone(result.n.members)
			}
			members = append(members, m)
		}
		if members != nil {
			result.n = newObject(members)
		}
		return result, nil
	case KindRegex:
		result, err := inner()
		if err != nil {
			return result, err
		}
		if kindOf(result.n) != '"' {
			return fail("expected string for pattern")
		}
		if !s.Regexp.MatchString(result.n.text()) {
			return fail("string does not match pattern")
		}
		return result, nil
	case KindMin, KindMax, KindGte, KindLte:
		result, err := inner()
		if err != nil {
			return result, err
		}
		var value float64
		switch kindOf(result.n) {
		case '0':
			var ok bool
			if value, ok = result.n.number(); !ok {
				return fail("invalid numeric value")
			}
		case '"':
			value = float64(utf8.RuneCountInString(result.n.text()))
		case '[':
			value = float64(len(result.n.elements))
		default:
			return fail("bound applied to unsupported value")
		}
		if (s.Kind == KindMin || s.Kind == KindGte) && value < s.bound {
			return fail(fmt.Sprintf("value or length must be >= %g", s.bound))
		}
		if (s.Kind == KindMax || s.Kind == KindLte) && value > s.bound {
			return fail(fmt.Sprintf("value or length must be <= %g", s.bound))
		}
		return result, nil
	case KindInt:
		value := outcome{n: n}
		var err error
		if s.Inner != nil {
			value, err = inner()
			if err != nil {
				return value, err
			}
		}
		if kindOf(value.n) != '0' {
			return fail("expected a safe integer")
		}
		f, ok := value.n.number()
		if !ok || math.Trunc(f) != f || math.Abs(f) > 9007199254740991 {
			return fail("expected a safe integer")
		}
		// JSON numbers such as 1.0 and 1e0 are integers to JavaScript/Zod,
		// but must be normalized before decoding into a Go integer type.
		if !plainInt(value.n.raw) {
			text := []byte("0")
			if f != 0 {
				text = strconv.AppendFloat(nil, f, 'f', 0, 64)
			}
			value.n = &node{kind: '0', raw: text}
		}
		return value, nil
	}
	if n == nil {
		return fail("required value is missing")
	}
	switch s.Kind {
	case KindUnknown, KindAny:
		// Any present value; an absent one, as Zod has it, failed above.
	case KindNull:
		if n.kind != 'n' {
			return fail("expected null")
		}
	case KindString:
		if n.kind != '"' {
			return fail("expected string")
		}
	case KindBoolean:
		if n.kind != 't' && n.kind != 'f' {
			return fail("expected boolean")
		}
	case KindNumber:
		f, ok := 0.0, false
		if n.kind == '0' {
			f, ok = n.number()
		}
		if !ok || math.IsInf(f, 0) || math.IsNaN(f) {
			return fail("expected finite number")
		}
	case KindLiteral:
		if !s.matches(n) {
			return outcome{}, &ruleError{path: path.clone(), message: "expected literal", value: s.Value}
		}
	case KindURL:
		if n.kind != '"' {
			return fail("expected string")
		}
		parsed, err := url.Parse(strings.TrimSpace(n.text()))
		if err != nil || parsed.Scheme == "" {
			return fail("expected absolute URL")
		}
		switch strings.ToLower(parsed.Scheme) {
		case "http", "https", "ftp", "ws", "wss":
			if parsed.Hostname() == "" {
				return fail("URL requires a host")
			}
		}
	case KindDateTime:
		if n.kind != '"' {
			return fail("expected string")
		}
		text := n.text()
		if !datetimePattern.MatchString(text) {
			return fail("expected ISO datetime")
		}
		if _, err := time.Parse(time.RFC3339Nano, text); err != nil {
			return fail("invalid datetime")
		}
		if !s.Offset && !strings.HasSuffix(text, "Z") {
			return fail("datetime offset is not allowed")
		}
	case KindArray, KindSkipArray:
		if n.kind != '[' {
			return fail("expected array")
		}
		var output []*node // nil while every element is unchanged
		for i, element := range n.elements {
			at := jsonPath{parent: path, index: i, element: true}
			result, err := r.apply(s.Inner, element, &at, depth+1)
			if err == nil && result.n == nil {
				result.n = &node{kind: 'n', raw: []byte("null")}
			}
			if output == nil && (err != nil || result.n != element) {
				output = append(make([]*node, 0, len(n.elements)), n.elements[:i]...)
			}
			if err != nil {
				if s.Kind == KindSkipArray {
					continue
				}
				return outcome{}, err
			}
			if output != nil {
				output = append(output, result.n)
			}
		}
		if output == nil {
			return pass()
		}
		return outcome{n: &node{kind: '[', elements: output}}, nil
	case KindObject:
		if n.kind != '{' {
			return fail("expected object")
		}
		// The output has each field that has a value, and none of the
		// input's other properties. output stays nil, and the input is the
		// output, while every field comes back as it was.
		var output []member
		kept := 0
		for i, field := range s.Fields {
			input := n.get(field.Name)
			at := jsonPath{parent: path, name: field.Name}
			value, err := r.apply(field.Schema, input, &at, depth+1)
			if err != nil {
				return outcome{}, err
			}
			if output == nil && value.n != input {
				output = make([]member, 0, len(s.Fields))
				for _, prev := range s.Fields[:i] {
					if v := n.get(prev.Name); v != nil {
						output = append(output, member{prev.Name, v})
					}
				}
			}
			switch {
			case output != nil && value.n != nil:
				output = append(output, member{field.Name, value.n})
			case output == nil && input != nil:
				kept++
			}
		}
		if output == nil && (s.Loose || kept == len(n.members)) {
			return outcome{n: n, object: s}, nil
		}
		if output == nil { // only undeclared properties were dropped
			for _, field := range s.Fields {
				if v := n.get(field.Name); v != nil {
					output = append(output, member{field.Name, v})
				}
			}
		}
		if s.Loose { // a field changed: keep the undeclared properties too
			for _, m := range n.members {
				if !s.declares(m.name) {
					output = append(output, m)
				}
			}
		}
		return outcome{n: newObject(output), object: s}, nil
	case KindRecord:
		if n.kind != '{' {
			return fail("expected object")
		}
		// Keys in sorted order, so the first invalid one reported does not
		// depend on the input's order.
		output := make([]member, 0, len(n.members))
		unchanged := true
		for _, m := range slices.SortedFunc(slices.Values(n.members), func(a, b member) int { return strings.Compare(a.name, b.name) }) {
			at := jsonPath{parent: path, name: m.name}
			// Every property name is a string, so a string key rule, the
			// only kind the SDK has, cannot fail.
			if s.Key.Kind != KindString {
				if _, err := r.apply(s.Key, keyNode(m.name), &at, depth+1); err != nil {
					return outcome{}, err
				}
			}
			value, err := r.apply(s.Inner, m.value, &at, depth+1)
			if err != nil {
				return outcome{}, err
			}
			if value.n != nil {
				output = append(output, member{m.name, value.n})
			}
			unchanged = unchanged && value.n == m.value
		}
		if unchanged {
			return outcome{n: n, record: true}, nil
		}
		return outcome{n: newObject(output), record: true}, nil
	default:
		return fail(fmt.Sprintf("unsupported generated Zod rule %d", s.Kind))
	}
	return pass()
}

// plainInt reports whether an integer's JSON text is already as Go decodes
// integers: no fraction, exponent, leading zero or negative zero.
func plainInt(raw []byte) bool {
	digits := bytes.TrimPrefix(raw, []byte("-"))
	if len(digits) == 0 || (digits[0] == '0' && (len(digits) > 1 || len(digits) < len(raw))) {
		return false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// matches reports whether n equals the literal s.
func (s *Rule) matches(n *node) bool {
	// A string without escapes is already in canonical form.
	if n.kind == '"' && s.lit[0] == '"' && !slices.Contains(n.raw, '\\') {
		return string(n.raw) == string(s.lit)
	}
	return Equal(encode(nil, n), s.lit)
}

// keyNode is a record key as a string value for the key rule.
func keyNode(name string) *node {
	raw, _ := jsontext.AppendQuote(nil, name)
	return &node{kind: '"', raw: raw}
}

// tagOf returns the decoded string property name of an object, or false when
// n is not an object or has no such string property. The text is the
// input's own bytes unless the string has escapes.
func tagOf(n *node, name string) ([]byte, bool) {
	if kindOf(n) != '{' {
		return nil, false
	}
	v := n.get(name)
	if kindOf(v) != '"' {
		return nil, false
	}
	if body := v.raw[1 : len(v.raw)-1]; !slices.Contains(body, '\\') {
		return body, true
	}
	return []byte(v.text()), true
}

// hasTag reports whether tag is one of tags.
func hasTag(tags []string, tag []byte) bool {
	return slices.ContainsFunc(tags, func(t string) bool { return t == string(tag) })
}

// merge combines the results of an intersection's members.
func merge(a, b *node, path *jsonPath) (*node, error) {
	if same(a, b) {
		return a, nil
	}
	switch {
	case kindOf(a) == '{' && kindOf(b) == '{':
		members := slices.Clone(a.members)
		for _, m := range b.members {
			i := slices.IndexFunc(members, func(x member) bool { return x.name == m.name })
			if i < 0 {
				members = append(members, m)
				continue
			}
			at := jsonPath{parent: path, name: m.name}
			merged, err := merge(members[i].value, m.value, &at)
			if err != nil {
				return nil, err
			}
			members[i].value = merged
		}
		return newObject(members), nil
	case kindOf(a) == '[' && kindOf(b) == '[' && len(a.elements) == len(b.elements):
		elements := make([]*node, len(a.elements))
		for i := range elements {
			at := jsonPath{parent: path, index: i, element: true}
			merged, err := merge(a.elements[i], b.elements[i], &at)
			if err != nil {
				return nil, err
			}
			elements[i] = merged
		}
		return &node{kind: '[', elements: elements}, nil
	}
	return nil, fmt.Errorf("%s: incompatible intersection results", path.String())
}

// same reports whether two values are canonically equal JSON.
func same(a, b *node) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a == b || Equal(encode(nil, a), encode(nil, b))
}

// declares reports whether the object rule s has a field named name.
func (s *Rule) declares(name string) bool {
	return slices.ContainsFunc(s.Fields, func(f Field) bool { return f.Name == name })
}
