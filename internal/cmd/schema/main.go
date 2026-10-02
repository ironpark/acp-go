package main

import (
	"bytes"
	_ "embed"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/ironpark/acp-go/internal/cmd/schema/facade"
	"github.com/ironpark/acp-go/internal/cmd/schema/tsdef"
	"github.com/ironpark/acp-go/internal/cmd/schema/tsgen"
)

// overridesYAML corrects the TypeScript schema where it says less than the
// protocol means; see the file for what it holds.
//
//go:embed overrides.yaml
var overridesYAML []byte

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string) error {
	flags := flag.NewFlagSet("acp-schema", flag.ContinueOnError)
	source := flags.String("source", "schema/typescript", "Directory containing v1 and v2 TypeScript schema snapshots")
	output := flags.String("out", "schema", "Output directory for v1/*.gen.go and v2/*.gen.go")
	facadeRoot := flags.String("facade", "", "Module root to write the acp1 and acp2 façade files into; skipped when empty")
	check := flags.Bool("check", false, "Check generated files without writing")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	// Complete parsing and generation of both versions before modifying outputs.
	var results []result
	overrides, err := tsdef.ParseOverrides(overridesYAML)
	if err != nil {
		return err
	}
	applied := map[string]bool{}
	specs := map[string]*facade.Spec{"v1": facade.V1, "v2": facade.V2}
	for _, version := range []string{"v1", "v2"} {
		schema, err := tsdef.ParseDir(filepath.Join(*source, version))
		if err != nil {
			return fmt.Errorf("%s: %w", version, err)
		}
		used, err := overrides.Apply(schema)
		if err != nil {
			return fmt.Errorf("%s: %w", version, err)
		}
		maps.Copy(applied, used)
		files, decls, err := tsgen.Generate(schema, "schema")
		if err != nil {
			return fmt.Errorf("%s: %w", version, err)
		}
		for _, name := range slices.Sorted(maps.Keys(files)) {
			results = append(results, result{filepath.Join(*output, version, name), files[name]})
		}
		if *facadeRoot == "" {
			continue
		}
		spec := specs[version]
		facadeFiles, err := facade.Generate(spec, schema, files, decls)
		if err != nil {
			return fmt.Errorf("%s façade: %w", version, err)
		}
		for _, name := range slices.Sorted(maps.Keys(facadeFiles)) {
			results = append(results, result{filepath.Join(*facadeRoot, spec.Dir, name), facadeFiles[name]})
		}
	}
	for _, key := range slices.Sorted(maps.Keys(overrides.Numbers)) {
		if !applied[key] {
			return fmt.Errorf("overrides: numbers.%s names nothing in any schema version", key)
		}
	}
	// A result is stale when its file differs. Every *.gen.go in an output
	// directory is ours: one the generator no longer produces is an orphan.
	// -check reports both; a write replaces the one and removes the other.
	produced := map[string]bool{}
	for _, r := range results {
		produced[r.path] = true
	}
	var stale []result
	var orphans []string
	globbed := map[string]bool{}
	for _, r := range results {
		if existing, err := os.ReadFile(r.path); err != nil || !bytes.Equal(existing, r.data) {
			stale = append(stale, r)
		}
		dir := filepath.Dir(r.path)
		if globbed[dir] {
			continue
		}
		globbed[dir] = true
		matches, err := filepath.Glob(filepath.Join(dir, "*.gen.go"))
		if err != nil {
			return err
		}
		for _, path := range matches {
			if !produced[path] {
				orphans = append(orphans, path)
			}
		}
	}
	if *check {
		if len(stale) > 0 {
			return fmt.Errorf("%s is stale; run go generate ./...", stale[0].path)
		}
		if len(orphans) > 0 {
			return fmt.Errorf("%s is no longer generated; run go generate ./...", orphans[0])
		}
		return nil
	}
	if err := write(stale); err != nil {
		return err
	}
	for _, path := range orphans {
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}

// result is one generated file and where it goes.
type result struct {
	path string
	data []byte
}

// write stages every file next to its destination before renaming any, so a
// failed write leaves the previous output in place.
func write(results []result) error {
	type staged struct{ temp, path string }
	var pending []staged
	defer func() {
		for _, p := range pending {
			os.Remove(p.temp) // a no-op once renamed
		}
	}()
	for _, r := range results {
		dir := filepath.Dir(r.path)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
		f, err := os.CreateTemp(dir, "."+filepath.Base(r.path)+".*")
		if err != nil {
			return err
		}
		pending = append(pending, staged{f.Name(), r.path})
		_, err = f.Write(r.data)
		if err == nil {
			err = f.Chmod(0644)
		}
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
	}
	for _, p := range pending {
		if err := os.Rename(p.temp, p.path); err != nil {
			return err
		}
	}
	return nil
}
