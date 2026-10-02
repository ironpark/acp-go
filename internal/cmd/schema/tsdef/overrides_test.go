package tsdef

import "testing"

func TestOverrides(t *testing.T) {
	schema, err := Parse("fixture.ts", []byte(`export type Usage = { total: number; cached?: number | null; label: string; }; export type Id = null | number | string;`))
	if err != nil {
		t.Fatal(err)
	}
	o, err := ParseOverrides([]byte("numbers:\n  Usage.total: uint64\n  Usage.cached: int64\n  Id: int64\n  Missing.field: uint64\n"))
	if err != nil {
		t.Fatal(err)
	}
	applied, err := o.Apply(schema)
	if err != nil {
		t.Fatal(err)
	}
	if !applied["Usage.total"] || !applied["Usage.cached"] || !applied["Id"] || applied["Missing.field"] {
		t.Fatalf("applied %v", applied)
	}
	fields := schema.Types[0].Type.Fields
	if fields[0].Type.Number != "uint64" || numberMember(fields[1].Type).Number != "int64" {
		t.Fatalf("numbers not set: %+v %+v", fields[0].Type, fields[1].Type)
	}

	if numberMember(schema.Types[1].Type).Number != "int64" {
		t.Fatal("definition alternative not set")
	}
	for _, key := range []string{"Usage.label", "Usage", "Usage.total"} {
		if _, err := (&Overrides{Numbers: map[string]string{key: "uint64"}}).Apply(schema); err == nil {
			t.Errorf("%s: applied to a string, an object or an integer already typed", key)
		}
	}
	for _, bad := range []string{"numbers:\n  Usage.total: float32\n", "numbers:\n  .total: uint64\n", "numbers:\n  Usage.: uint64\n", "strings: {}\n"} {
		if _, err := ParseOverrides([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
