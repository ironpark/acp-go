package tsdef

import (
	"strings"
	"testing"
)

func hinted(t *testing.T, types, zod string) (*Schema, error) {
	t.Helper()
	s, err := Parse("types.ts", []byte(types))
	if err != nil {
		t.Fatal(err)
	}
	s.Validators, err = ParseZod("zod.ts", []byte(zod))
	if err != nil {
		t.Fatal(err)
	}
	return s, ApplyNumericHints(s)
}

func TestNumericHints(t *testing.T) {
	s, err := hinted(t,
		`export type Version = number; export type Request = {line?: number | null; cost: number; count: number;};
		 export type Error = {code: number; lines: number[]; extra: Version};`,
		`export const zVersion = z.int().gte(0).lte(65535);
		 export const zRequest = z.object({line: defaultOnError(z.number().int().gte(0).max(4294967295).nullish(),()=>undefined),cost:z.number(),count:z.int()});
		 export const zError = z.looseObject({code: z.int().min(-2147483648).max(2147483647), lines: vecSkipError(z.int().gte(0)), extra: zVersion});`)
	if err != nil {
		t.Fatal(err)
	}
	if s.Types[0].Type.Number != "uint16" {
		t.Fatal("missing protocol version range")
	}
	fields := s.Types[1].Type.Fields
	if fields[0].Type.Members[0].Number != "uint32" || fields[1].Type.Number != "" || fields[2].Type.Number != "int64" {
		t.Fatal("incorrect numeric representations")
	}
	fields = s.Types[2].Type.Fields
	if fields[0].Type.Number != "int32" || fields[1].Type.Element.Number != "uint64" {
		t.Fatalf("loose object or array element: got %q, %q", fields[0].Type.Number, fields[1].Type.Element.Number)
	}
}

func TestNumericHintsRejectUnpairedInteger(t *testing.T) {
	// The rule says integer, but the type has no number for it to reach.
	_, err := hinted(t, `export type Request = {count: string};`, `export const zRequest = z.object({count: z.int()});`)
	if err == nil || !strings.Contains(err.Error(), "zRequest") {
		t.Fatalf("want an unpaired integer error naming zRequest, got %v", err)
	}
}
