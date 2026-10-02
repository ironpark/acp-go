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
	applied, err := o.Apply(schema, "v1")
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
		if _, err := (&Overrides{Numbers: map[string]string{key: "uint64"}}).Apply(schema, "v1"); err == nil {
			t.Errorf("%s: applied to a string, an object or an integer already typed", key)
		}
	}
	for _, bad := range []string{"numbers:\n  Usage.total: float32\n", "numbers:\n  .total: uint64\n", "numbers:\n  Usage.: uint64\n", "strings: {}\n"} {
		if _, err := ParseOverrides([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestTristateOverrides(t *testing.T) {
	schema, err := Parse("fixture.ts", []byte(`export type Patch = { title?: string | null; name: string | null; label?: string; raw?: unknown; };`))
	if err != nil {
		t.Fatal(err)
	}
	o := &Overrides{Tristate: map[string][]string{"v2": {"Patch.title"}, "v1": {"Gone.member"}}}
	if _, err := o.Apply(schema, "v2"); err != nil {
		t.Fatal(err)
	}
	if !schema.Types[0].Type.Fields[0].Tristate {
		t.Fatal("title not marked tristate")
	}
	// Each version checks its own list.
	if _, err := o.Apply(schema, "v1"); err == nil {
		t.Fatal("v1 accepted a member it lacks")
	}
	for _, member := range []string{"name", "label", "raw"} {
		bad := &Overrides{Tristate: map[string][]string{"v2": {"Patch." + member}}}
		if _, err := bad.Apply(schema, "v2"); err == nil {
			t.Errorf("Patch.%s: accepted a member that is not an optional, nullable, typed value", member)
		}
	}
}
