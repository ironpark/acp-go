package tsdef

import (
	"strings"
	"testing"
)

func TestParseSchemaSubset(t *testing.T) {
	source := []byte(`
 type Private = ` + "`${string}`" + `;
 /** A message. */
 export type Message = (Text & { kind: "text" }) | { kind: "custom"; [key: string]: unknown };
 export type Text = { /** Body. */ body: string; tags?: Array<string> | null; };
 export const METHODS = { session_new: "session/new" } as const;
 export const VERSION = 2;
 export type { Text } from "./types.gen.js";
 `)
	s, err := Parse("schema.ts", source)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Types) != 2 || len(s.Constants) != 2 {
		t.Fatalf("unexpected schema: %+v", s)
	}
	message := s.Types[0]
	if message.Comment != "A message." || message.Type.Kind != KindUnion || len(message.Type.Members) != 2 {
		t.Fatalf("lost union: %+v", message)
	}
	if message.Type.Members[0].Kind != KindIntersection {
		t.Fatal("lost intersection")
	}
	fields := s.Types[1].Type.Fields
	if fields[0].Comment != "Body." || !fields[1].Optional || fields[1].Type.Members[0].Kind != KindArray || fields[1].Type.Members[1].Kind != KindNull {
		t.Fatalf("lost field semantics: %+v", fields)
	}
	if s.Constants[0].Members[0].Value != `"session/new"` || s.Constants[1].Value != "2" {
		t.Fatal("lost constants")
	}
}
func TestRejectUnsupportedSchema(t *testing.T) {
	for _, source := range []string{`export type X = { value };`, `export type X = Pick<Foo, "bar">;`, `export type X = [string, number];`, `export type X = { run(): void };`, `export type X = { value: ; };`, `export type Box<T> = { value: T };`} {
		_, err := Parse("input.ts", []byte(source))
		if err == nil || !strings.Contains(err.Error(), "input.ts:1:") {
			t.Fatalf("expected located error for %q, got %v", source, err)
		}
	}
}

// The SDK brands received catch-alls and narrows custom tags with template
// literals; neither changes the JSON, so both catch-alls are one alternative.
func TestBrandedCatchAll(t *testing.T) {
	s, err := Parse("input.ts", []byte(`
 export type UnknownVariant<T> = T & { readonly [unknownVariant]: true };
 export type X = { type: "a" } | { /** custom */ type: `+"`_${string}`"+`; [key: string]: unknown } | UnknownVariant<{ type: string; [key: string]: unknown }>;
 `))
	if err != nil {
		t.Fatal(err)
	}
	members := s.Types[0].Type.Members
	if len(s.Types) != 1 || len(members) != 2 || members[1].Fields[0].Type.Kind != KindString || members[1].Element == nil {
		t.Fatalf("got %d types, members %+v", len(s.Types), members)
	}
}

func TestTreeOwnership(t *testing.T) {
	source := []byte(`export type X = { name: string };`)
	s, err := Parse("input.ts", source)
	if err != nil {
		t.Fatal(err)
	}
	for i := range source {
		source[i] = ' '
	}
	if s.Types[0].Name != "X" || s.Types[0].Type.Fields[0].Name != "name" {
		t.Fatal("schema refers to parser-owned memory")
	}
}
