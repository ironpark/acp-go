package tsdef

import (
	"encoding/json/v2"
	"fmt"
	"maps"
	"math"
	"slices"
)

// ApplyNumericHints gives each number the Go integer type its Zod rule in
// schema.Validators implies, walking every definition's type alongside its
// rule. It reads only integer builders and bounds, not Zod's validation,
// defaults or recovery. Every integer rule must reach a number: one the walk
// cannot pair with its type fails, rather than leaving a float64 behind.
func ApplyNumericHints(schema *Schema) error {
	h := hinter{validators: schema.Validators, used: map[*Zod]bool{}}
	for _, d := range schema.Types {
		if z := schema.Validators["z"+d.Name]; z != nil {
			h.hint(d.Type, z)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(schema.Validators)) {
		var missed error
		walkZod(schema.Validators[name], func(z *Zod) {
			if z.Kind == "int" && !h.used[z] && missed == nil {
				missed = fmt.Errorf("%s: integer rule matches no number of the TypeScript type", name)
			}
		})
		if missed != nil {
			return missed
		}
	}
	return nil
}

type hinter struct {
	validators map[string]*Zod
	used       map[*Zod]bool // integer rules a number took its type from
}

// hint pairs t with z. A reference on either side is its own definition's
// walk, so it ends here.
func (h hinter) hint(t *Type, z *Zod) {
	if t.Kind == KindRef || z.Kind == "ref" {
		return
	}
	switch t.Kind {
	case KindUnion, KindIntersection:
		// z.union([...]) members align positionally with the TypeScript union.
		if u := unwrapZod(z); t.Kind == KindUnion && u.Kind == "union" && len(u.Members) == len(t.Members) {
			for i, m := range t.Members {
				h.hint(m, u.Members[i])
			}
			return
		}
		for _, m := range t.Members {
			h.hint(m, z)
		}
	case KindNumber:
		h.number(t, z)
	case KindObject:
		for _, o := range reachable(z, "object") {
			for _, zf := range o.Fields {
				for i := range t.Fields {
					if t.Fields[i].Name == zf.Name {
						h.hint(t.Fields[i].Type, zf.Schema)
					}
				}
			}
		}
		if t.Element != nil {
			for _, r := range reachable(z, "record") {
				h.hint(t.Element, r.Inner)
			}
		}
	case KindArray:
		for _, a := range reachable(z, "array", "skipArray") {
			h.hint(t.Element, a.Inner)
		}
	}
}

// number reads the integer builder and bounds wrapped around z's base rule.
func (h hinter) number(t *Type, z *Zod) {
	var integer *Zod
	low, high := math.Inf(-1), math.Inf(1)
	for ; z != nil; z = z.Inner {
		var bound float64
		switch z.Kind {
		case "int":
			integer = z
		case "min", "gte":
			if json.Unmarshal(z.Value, &bound) == nil {
				low = max(low, bound)
			}
		case "max", "lte":
			if json.Unmarshal(z.Value, &bound) == nil {
				high = min(high, bound)
			}
		}
	}
	if integer == nil {
		return
	}
	h.used[integer] = true
	t.Number = integerType(low, high)
}

// integerType is the narrowest of the Go types the generator uses that holds
// every integer in [low, high].
func integerType(low, high float64) string {
	switch {
	case low >= 0 && high <= math.MaxUint16:
		return "uint16"
	case low >= 0 && high <= math.MaxUint32:
		return "uint32"
	case low >= 0:
		return "uint64"
	case low >= math.MinInt32 && high <= math.MaxInt32:
		return "int32"
	}
	return "int64"
}

// unwrapZod returns the rule the wrappers around z (optional, nullable,
// defaults, recovery, bounds) apply to.
func unwrapZod(z *Zod) *Zod {
	for z.Inner != nil && z.Kind != "array" && z.Kind != "skipArray" && z.Kind != "record" {
		z = z.Inner
	}
	return z
}

// reachable returns the rules of the given kinds z is built from through
// wrappers, intersections and unions. It crosses no reference and no other
// container, whose contents describe another value.
func reachable(z *Zod, kinds ...string) []*Zod {
	if slices.Contains(kinds, z.Kind) {
		return []*Zod{z}
	}
	switch z.Kind {
	case "ref", "object", "array", "skipArray", "record":
		return nil
	}
	var out []*Zod
	for _, c := range z.Children() {
		out = append(out, reachable(c, kinds...)...)
	}
	return out
}

// walkZod calls visit on z and every rule nested in it.
func walkZod(z *Zod, visit func(*Zod)) {
	visit(z)
	for _, c := range z.Children() {
		walkZod(c, visit)
	}
}
