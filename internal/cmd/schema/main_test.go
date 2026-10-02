package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/acp-go/internal/cmd/schema/tsdef"
)

func TestPinnedSDKGeneration(t *testing.T) {
	source := "../../../schema/typescript"
	for _, version := range []string{"v1", "v2"} {
		s, err := tsdef.ParseDir(filepath.Join(source, version))
		if err != nil {
			t.Fatal(err)
		}
		if len(s.Types) == 0 || len(s.Constants) != 4 || len(s.Validators) != len(s.Types) {
			t.Fatalf("%s: unexpected snapshot: %d types, %d validators, %d constants", version, len(s.Types), len(s.Validators), len(s.Constants))
		}
		for _, d := range s.Types {
			if s.Validators["z"+d.Name] == nil {
				t.Fatalf("%s: %s has no Zod schema", version, d.Name)
			}
		}
	}
	output := t.TempDir()
	args := []string{"-source", source, "-out", output}
	if err := run(args); err != nil {
		t.Fatal(err)
	}
	if err := run(append(args, "-check")); err != nil {
		t.Fatal(err)
	}
	// Every checked-in schema file, and no other, is the generator's output.
	if err := run([]string{"-source", source, "-out", "../../../schema", "-check"}); err != nil {
		t.Fatal(err)
	}
	// The façade files are checked in at the module root; they cannot compile
	// standalone, so stale detection against the repository is their test.
	if err := run(append(args, "-facade", "../../..", "-check")); err != nil {
		t.Fatal(err)
	}
	// A second run leaves unchanged files alone.
	unchanged := filepath.Join(output, "v1", "enums.gen.go")
	past := time.Unix(1_000_000_000, 0)
	if err := os.Chtimes(unchanged, past, past); err != nil {
		t.Fatal(err)
	}
	if err := run(args); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(unchanged); err != nil || !info.ModTime().Equal(past) {
		t.Fatalf("regeneration rewrote an unchanged file: %v", err)
	}
	orphan := filepath.Join(output, "v1", "removed.gen.go")
	if err := os.WriteFile(orphan, []byte("package schema\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := run(append(args, "-check")); err == nil {
		t.Fatal("check accepted a generated file the generator no longer produces")
	}
	if err := run(args); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("generation kept %s: %v", orphan, err)
	}
	stale := filepath.Join(output, "v1", "types.gen.go")
	if err := os.WriteFile(stale, []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := run(append(args, "-check")); err == nil {
		t.Fatal("check accepted stale output")
	}
	got, err := os.ReadFile(stale)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "stale" {
		t.Fatal("check modified output")
	}
}
func TestNoPartialOutputOnParseFailure(t *testing.T) {
	source := t.TempDir()
	output := t.TempDir()
	for _, version := range []string{"v1", "v2"} {
		dir := filepath.Join(source, version)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		for name, content := range map[string]string{"types.gen.ts": "export type X = string;", "index.ts": "export const VERSION = 1;", "zod.gen.ts": ""} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(source, "v2", "types.gen.ts"), []byte("export type X = Pick<Y, 'z'>;"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"-source", source, "-out", output}); err == nil {
		t.Fatal("accepted unsupported type")
	}
	entries, err := os.ReadDir(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("wrote partial output")
	}
}

// TestTristateMatchesRustSchema checks overrides.yaml's tristate lists against
// the members the protocol's Rust schema declares MaybeUndefined, when the
// reference checkout is present. Raw JSON members (Value) keep null on their
// own and are not listed.
func TestTristateMatchesRustSchema(t *testing.T) {
	src := "../../../reference/agent-client-protocol/agent-client-protocol-schema/src"
	if _, err := os.Stat(src); err != nil {
		t.Skip("no reference checkout")
	}
	overrides, err := tsdef.ParseOverrides(overridesYAML)
	if err != nil {
		t.Fatal(err)
	}
	structRE := regexp.MustCompile(`^\s*pub (?:struct|enum) (\w+)`)
	fieldRE := regexp.MustCompile(`^\s*pub (\w+): MaybeUndefined<(.+)>,`)
	for _, version := range []string{"v1", "v2"} {
		files, err := filepath.Glob(filepath.Join(src, version, "*.rs"))
		if err != nil {
			t.Fatal(err)
		}
		var want []string
		for _, file := range files {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			owner := ""
			for line := range strings.Lines(string(data)) {
				if m := structRE.FindStringSubmatch(line); m != nil {
					owner = m[1]
				}
				m := fieldRE.FindStringSubmatch(line)
				if m == nil || owner == "" || strings.HasSuffix(m[2], "Value") {
					continue
				}
				member := camel(m[1])
				if member == "meta" {
					member = "_meta"
				}
				want = append(want, owner+"."+member)
			}
		}
		got := slices.Clone(overrides.Tristate[version])
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("tristate.%s:\n got %v\nwant %v", version, got, want)
		}
	}
}

// camel turns a Rust snake_case member into its serde camelCase name.
func camel(s string) string {
	parts := strings.Split(s, "_")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}
