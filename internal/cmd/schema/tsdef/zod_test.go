package tsdef

import (
	"strings"
	"testing"
)

func TestZodWrappersAndLiterals(t *testing.T) {
	schemas, err := ParseZod("zod.ts", []byte(`
 import * as z from "zod/v4";
 export const zText = z.string();
 export const zValue = z.object({
  label: zText.optional().default("value"),
  data: defaultOnError(z.record(z.string(),z.unknown()).nullish(),()=>undefined),
  entries: requiredDefaultOnError(vecSkipError(z.number().int()),()=>[]),
  options: defaultOnError(z.object({ enabled: z.boolean() }),()=>({enabled:false})),
  currency: z.string().regex(/^[A-Z]{3}$/),
  time: z.iso.datetime({offset:true}),
 });
 `))
	if err != nil {
		t.Fatal(err)
	}
	fields := schemas["zValue"].Fields
	label := fields[0].Schema
	if label.Kind != "default" || label.Inner.Kind != "optional" || label.Inner.Inner.Ref != "zText" || string(label.Value) != `"value"` {
		t.Fatalf("lost wrapper order: %+v", label)
	}
	if fields[1].Schema.Value != nil {
		t.Fatal("undefined fallback was converted to null")
	}
	if fields[2].Schema.Kind != "requiredCatch" || fields[2].Schema.Inner.Kind != "skipArray" {
		t.Fatal("lost required recovery")
	}
	if string(fields[3].Schema.Value) != `{"enabled":false}` {
		t.Fatal("lost literal object fallback")
	}
	if fields[4].Schema.Pattern != "^[A-Z]{3}$" || !fields[5].Schema.Offset {
		t.Fatal("lost string constraints")
	}
}
func TestZodLooseObject(t *testing.T) {
	schemas, err := ParseZod("zod.ts", []byte(`export const zError = z.looseObject({ code: z.int(), data: z.unknown().optional() });`))
	if err != nil {
		t.Fatal(err)
	}
	if z := schemas["zError"]; z.Kind != "object" || !z.Loose || len(z.Fields) != 2 {
		t.Fatalf("looseObject parsed as %+v", z)
	}
}

func TestUnsupportedZodFails(t *testing.T) {
	for _, source := range []string{
		`export const zX = z.string().transform(v=>v);`,
		`export const zX = z.string().regex(/foo/i);`,
		`export const zX = z.string().default(makeValue());`,
		`export const zX = z.object({...other});`,
		`export const zX = z.iso.datetime({local:true});`,
		`export const zX = z.number().gte("zero");`,
		`export const zX = custom(z.string());`,
		`export const zX = z.literal(["a","b"]);`,
	} {
		_, err := ParseZod("zod.ts", []byte(source))
		if err == nil || !strings.Contains(err.Error(), "zod.ts:1:") {
			t.Fatalf("expected source-located error for %s; got %v", source, err)
		}
	}
	if _, err := ParseZod("zod.ts", []byte(`export const zX = zMissing.optional();`)); err == nil {
		t.Fatal("accepted unresolved reference")
	}
}
