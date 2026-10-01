package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ironpark/acp-go/internal/cmd/schema/tsdef"
)

func TestPinnedSDKGeneration(t *testing.T) {
	source := "../../../schema/typescript"
	for version, count := range map[string]int{"v1": 276, "v2": 270} {
		s, err := tsdef.ParseDir(filepath.Join(source, version))
		if err != nil {
			t.Fatal(err)
		}
		if len(s.Types) != count || len(s.Constants) != 4 || len(s.Validators) != count {
			t.Fatalf("%s: unexpected snapshot: %d types, %d constants", version, len(s.Types), len(s.Constants))
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
	for _, version := range []string{"v1", "v2"} {
		for _, name := range []string{"methods.gen.go", "enums.gen.go", "types.gen.go", "unions.gen.go", "envelope.gen.go", "zod.gen.go"} {
			generated, err := os.ReadFile(filepath.Join(output, version, name))
			if err != nil {
				t.Fatal(err)
			}
			checkedIn, err := os.ReadFile(filepath.Join("../../../schema", version, name))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(generated, checkedIn) {
				t.Fatalf("%s/%s: checked-in output is stale; run go generate ./...", version, name)
			}
		}
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
