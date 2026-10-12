package tsgen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/ironpark/acp-go/internal/cmd/schema/tsdef"
)

func TestGeneratedZod(t *testing.T) {
	s, err := tsdef.Parse("types.ts", []byte(`
 export type Count = number;
 export type Currency = string;
 export type URL = string;
 export type Timestamp = string;
 export type Options = { count: Count; label?: string; required: Array<number>; url?: URL; };
 export type Text = { text: string; note?: string | null; };
 export type Message = ((Text & {kind: "text"}) | {kind: string; [key:string]:unknown;}) & {_meta?: {[key:string]:unknown}|null;};
 export type UnknownVariant<T> = T & { readonly [unknownVariant]: true };
 export type Failure = { error?: string | null; };
 export type Idle = (
  | { reason: "done" }
  | (Failure & { reason: "error" })
  | { reason: `+"`_${string}`"+`; [key: string]: unknown }
  | UnknownVariant<{ reason: string; [key: string]: unknown }>
  | { reason?: null }
 ) & { usage?: number | null; reason?: string | null; };
 `))
	if err != nil {
		t.Fatal(err)
	}
	s.Validators, err = tsdef.ParseZod("zod.ts", []byte(`
 export const zCount = z.int().gte(0).max(10);
 export const zCurrency = z.string().regex(/^[A-Z]{3}$/);
 export const zURL = z.url();
 export const zTimestamp = z.iso.datetime({offset:true});
 export const zOptions = z.object({
  count: zCount.default(3),
  label: z.string().optional().default("hello"),
  required: requiredDefaultOnError(vecSkipError(z.number().int()),()=>[]),
  url: defaultOnError(zURL.nullish(),()=>undefined),
 });
 export const zText = z.object({text:z.string(),note:defaultOnError(z.string().nullish(),()=>undefined)});
 export const zMessage = preserveCustomPayload(z.intersection(z.union([
  zText.and(z.object({kind:z.literal("text")})),
  excludeKnownTags(z.object({kind:z.string()}),"kind",["text"]),
 ]),z.object({_meta:defaultOnError(z.record(z.string(),z.unknown()).nullish(),()=>undefined)})),"kind",["text"]);
 export const zFailure = z.object({error:defaultOnError(z.string().nullish(),()=>undefined)});
 export const zIdle = defaultOnErrorOptionalStringTag(preserveCustomPayload(z.intersection(z.union([
  z.object({reason:z.literal("done")}),
  zFailure.and(z.object({reason:z.literal("error")})),
  excludeKnownTags(z.object({reason:z.string()}),"reason",["done","error"]),
  z.object({reason:z.null().optional()}),
 ]),z.object({usage:defaultOnError(z.number().nullish(),()=>undefined),reason:defaultOnError(z.string().nullish(),()=>undefined)})),"reason",["done","error"]),"reason");
 `))
	if err != nil {
		t.Fatal(err)
	}
	data, _, err := Generate(s, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	tests, err := os.ReadFile("testdata/zod_test.go.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := data["zod.gen.go"]; !ok {
		t.Fatalf("expected zod.gen.go, got %v", slices.Sorted(maps.Keys(data)))
	}
	dir := t.TempDir()
	writeFixture(t, dir, data, tests)
	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated Zod tests: %v\n%s", err, output)
	}
}

// The runtime evaluator is in another module, so nothing but this test ties
// its Kind constants to zodKinds: a kind added on one side only would fail
// generation or leave the runtime a rule it cannot evaluate.
func TestZodKindsMatchRuntime(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "../../../../schema/internal/zod/zod.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var runtime []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			for _, name := range spec.(*ast.ValueSpec).Names {
				if strings.HasPrefix(name.Name, "Kind") && name.Name != "KindInvalid" {
					runtime = append(runtime, name.Name)
				}
			}
		}
	}
	var generated []string
	for _, kind := range zodKinds {
		generated = append(generated, "Kind"+kind)
	}
	slices.Sort(runtime)
	slices.Sort(generated)
	if !slices.Equal(runtime, generated) {
		t.Fatalf("runtime kinds %v, generator kinds %v", runtime, generated)
	}
}
