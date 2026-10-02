// Package tsgen generates Go wire types from the ACP TypeScript schema subset.
package tsgen

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"maps"
	"slices"
	"strings"
	"unicode"

	"github.com/ironpark/acp-go/internal/cmd/schema/tsdef"
)

type generator struct {
	defs         map[string]*tsdef.Type
	docs         map[string]string      // TypeScript name -> the definition's comment
	refs         map[string]int         // TypeScript name -> references to it across the schema
	absorbed     map[string]*absorption // Go name -> the tagged union that took the type over
	decls        Decls                  // Go name -> what else an enum or tagged union declares
	pending      []tsdef.Definition
	names        map[string]bool
	aliases      map[string]bool        // Go names declared with "type X = ..."
	unmarshalers []string               // json.UnmarshalFromFunc entries for variant interfaces
	openTags     map[string]openTags    // Go union name -> tags known to its Unknown variant
	usesMeta     bool                   // some struct has a _meta field typed as Meta
	getters      []getter               // pointer fields of payload structs, emitted last
	wires        map[string]bool        // tag names whose generic variant wire is declared, emitted last
	forms        map[*tsdef.Type]formed // memoized form results
	pkg          string
	buffers      map[string]*bytes.Buffer // output file name -> source being built
	order        []string                 // buffer creation order, for deterministic output
	out          *bytes.Buffer            // buffer the next write goes to
}

// Output file names. Declarations are split by kind so that the payload
// structs a caller reads are not interleaved with the JSON-RPC envelope
// unions the SDK uses internally.
const (
	fileMethods  = "methods.gen.go"  // method constants and the protocol version
	fileEnums    = "enums.gen.go"    // scalar identifier types and literal enums
	fileTypes    = "types.gen.go"    // object structs and aliases
	fileUnions   = "unions.gen.go"   // tagged and raw payload unions, plus the shared helpers
	fileEnvelope = "envelope.gen.go" // JSON-RPC envelope types: requests, responses, ids, errors
	fileGetters  = "getters.gen.go"  // nil-safe getters for pointer fields
	fileZod      = "zod.gen.go"      // Zod rule tables and Validated/Decode/Validate
)

// envelopePrefixes start the names of the JSON-RPC envelope unions (every
// message one side sends, as opposed to the payload of a single method), of
// the types declared for their members, and of the request id.
var envelopePrefixes = []string{"AgentRequest", "AgentResponse", "AgentNotification", "ClientRequest", "ClientResponse", "ClientNotification", "ProtocolLevelNotification", "RequestID"}

// Envelope reports whether a type belongs to the JSON-RPC envelope rather
// than to a method payload: an envelope union, a type declared for one of its
// members, the request id or the error object.
func Envelope(name string) bool {
	for _, prefix := range envelopePrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return name == "Error"
}

// use directs subsequent writes to the named output file, starting it with
// its header on first use.
func (g *generator) use(name string) {
	if buf, ok := g.buffers[name]; ok {
		g.out = buf
		return
	}
	buf := &bytes.Buffer{}
	g.buffers[name] = buf
	g.order = append(g.order, name)
	g.out = buf
}

// countRefs adds the references t makes to named types into refs.
func countRefs(t *tsdef.Type, refs map[string]int) {
	if t == nil {
		return
	}
	if t.Kind == tsdef.KindRef {
		refs[t.Name]++
	}
	for _, m := range t.Members {
		countRefs(m, refs)
	}
	for _, f := range t.Fields {
		countRefs(f.Type, refs)
	}
	countRefs(t.Element, refs)
}

// Files maps generated file names to their formatted Go source.
type Files map[string][]byte

// newGenerator registers every definition under its Go name so references
// resolve before any declaration is emitted.
func newGenerator(schema *tsdef.Schema, pkg string) (*generator, error) {
	g := &generator{
		defs: map[string]*tsdef.Type{}, docs: map[string]string{}, refs: map[string]int{},
		absorbed: map[string]*absorption{}, decls: Decls{},
		forms: map[*tsdef.Type]formed{}, wires: map[string]bool{},
		names: map[string]bool{}, aliases: map[string]bool{}, openTags: map[string]openTags{},
		pkg: pkg, buffers: map[string]*bytes.Buffer{},
	}
	for _, d := range schema.Types {
		name := Name(d.Name)
		if g.names[name] {
			return nil, fmt.Errorf("duplicate Go type %s", name)
		}
		g.names[name] = true
		g.defs[d.Name] = d.Type
		g.docs[d.Name] = d.Comment
		countRefs(d.Type, g.refs)
		d.Name = name
		g.pending = append(g.pending, d)
	}
	if err := g.planVariants(); err != nil {
		return nil, err
	}
	return g, nil
}

// Generate emits deterministic Go declarations. Unknown JSON payloads are kept
// as jsontext.Value, so custom/future variants survive a decode/encode cycle.
// Wire types are split by kind into methods, enums, types, unions and envelope
// files; Zod rules and Decode/Validate functions, when the schema has
// validators, go to zod.gen.go. It also returns the [Decls] of the generated
// package, for packages that re-export its types.
func Generate(schema *tsdef.Schema, pkg string) (Files, Decls, error) {
	g, err := generate(schema, pkg)
	if err != nil {
		return nil, nil, err
	}
	files := Files{}
	for _, name := range g.order {
		if err := g.flush(files, name); err != nil {
			return nil, nil, err
		}
	}
	return files, g.decls, nil
}

// Decl lists the identifiers a schema type brings with it: an enum's
// constants; a tagged union's variant interface and constraint, constructor
// and variant types; or a raw union's alternative constraint and constructor,
// which returns an error. Only tagged unions have an Interface.
type Decl struct {
	Constants   []string
	Interface   string
	Constraint  string
	Constructor string
	Variants    []string
}

// Decls maps Go type names to the extra identifiers Generate declares for each
// enum and union.
type Decls map[string]Decl

// generate emits every declaration into the generator's buffers.
func generate(schema *tsdef.Schema, pkg string) (*generator, error) {
	if !token.IsIdentifier(pkg) || token.Lookup(pkg).IsKeyword() || pkg == "_" {
		return nil, fmt.Errorf("invalid package name %q", pkg)
	}
	g, err := newGenerator(schema, pkg)
	if err != nil {
		return nil, err
	}
	g.use(fileMethods)
	for _, c := range schema.Constants {
		if err := g.constant(c); err != nil {
			return nil, err
		}
	}
	for i := 0; i < len(g.pending); i++ {
		if err := g.definition(g.pending[i]); err != nil {
			return nil, fmt.Errorf("%s: %w", g.pending[i].Name, err)
		}
	}
	if g.usesMeta {
		if err := g.reserve("Meta"); err != nil {
			return nil, err
		}
		g.use(fileTypes)
		g.write("\n// Meta is the _meta extension object reserved on protocol messages.\ntype Meta = meta.Meta\n")
	}
	if len(g.unmarshalers) > 0 {
		g.use(fileUnions)
		g.write("\n// Unmarshalers returns the unmarshalers that decode the tagged-union variant\n// interfaces directly, for callers that declare fields of those interface\n// types instead of the wrapper structs:\n//\n//\tjson.Unmarshal(data, &v, json.WithUnmarshalers(schema.Unmarshalers()))\nfunc Unmarshalers() *json.Unmarshalers { return unmarshalers }\n\nvar unmarshalers = json.JoinUnmarshalers(\n%s,\n)\n", strings.Join(g.unmarshalers, ",\n"))
	}
	g.emitTaggedWires()
	g.emitGetters()
	g.use(fileZod)
	if err := g.zod(schema); err != nil {
		return nil, err
	}
	return g, nil
}

// constantNames renames SDK constants whose derived name would read poorly.
var constantNames = map[string]string{"PROTOCOL_VERSION": "CurrentProtocolVersion"}

// constantName is the Go name of an SDK constant or constant table.
func constantName(c string) string {
	if override, ok := constantNames[c]; ok {
		return override
	}
	return Name(c)
}

// ConstantName is the Go name of a member of an SDK constant table, such as
// AgentMethodsSessionNew for AGENT_METHODS.session_new.
func ConstantName(table, member string) string {
	return constantName(table) + Name(member)
}

// constant emits an SDK constant. A table of members, such as AGENT_METHODS,
// becomes one const block named by [ConstantName].
func (g *generator) constant(c tsdef.Constant) error {
	name := constantName(c.Name)
	if len(c.Members) == 0 {
		if err := g.reserve(name); err != nil {
			return err
		}
		g.write("\n// %s is the SDK's %s constant.\nconst %s = %s\n", name, c.Name, name, c.Value)
		return nil
	}
	g.write("\n// The %s constants mirror the SDK's %s table.\nconst (\n", name, c.Name)
	for _, m := range c.Members {
		member := ConstantName(c.Name, m.Name)
		if err := g.reserve(member); err != nil {
			return err
		}
		g.write("%s = %s\n", member, m.Value)
	}
	g.write(")\n")
	return nil
}

// flush formats the buffered source into files[name] and resets the buffer.
func (g *generator) flush(files Files, name string) error {
	buf := g.buffers[name]
	if buf.Len() == 0 {
		return nil
	}
	body := g.polishComments(buf.Bytes())
	imports, err := usedImports(body)
	if err != nil {
		return fmt.Errorf("scan %s: %w", name, err)
	}
	var src bytes.Buffer
	fmt.Fprintf(&src, "// Code generated by acp-schema from the TypeScript SDK; DO NOT EDIT.\n\npackage %s\n\n", g.pkg)
	if len(imports) > 0 {
		src.WriteString("import (\n")
		for i, path := range imports {
			if i > 0 && strings.Contains(path, ".") != strings.Contains(imports[i-1], ".") {
				src.WriteString("\n") // standard library first, then module packages
			}
			fmt.Fprintf(&src, "%q\n", path)
		}
		src.WriteString(")\n\n")
	}
	src.Write(body)
	result, err := format.Source(src.Bytes())
	if err != nil {
		return fmt.Errorf("format %s: %w", name, err)
	}
	files[name] = result
	buf.Reset()
	return nil
}

func (g *generator) write(f string, a ...any) { fmt.Fprintf(g.out, f, a...) }

// importPaths maps the package names generated code may reference to their
// import paths; flush imports exactly those a file uses.
var importPaths = map[string]string{
	"json": "encoding/json/v2", "jsontext": "encoding/json/jsontext", "errors": "errors", "fmt": "fmt",
	"reflect": "reflect", "regexp": "regexp",
	"union": UnionRuntime, "zod": ZodRuntime, "meta": MetaRuntime, "optional": OptionalRuntime,
}

// usedImports parses a body of declarations and returns the sorted import
// paths of the packages it selects from.
func usedImports(body []byte) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), "", append([]byte("package p\n"), body...), parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	used := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok {
				if path, ok := importPaths[id.Name]; ok {
					used[path] = true
				}
			}
		}
		return true
	})
	return slices.Sorted(maps.Keys(used)), nil
}

func comment(s string) string {
	if s == "" {
		return ""
	}
	return "// " + strings.ReplaceAll(s, "\n", "\n// ") + "\n"
}

// initialisms are the words Name writes in all capitals, following Go's
// convention that an initialism keeps one case (MCPServer, not McpServer):
// staticcheck's list plus the protocol's own. The façade method tables name
// their Go methods by hand and are tested against Name. Nes (next edit
// suggestions) is left out on purpose: it sits mid-name in most identifiers,
// where an all-capitals run is hard to read.
var initialisms = map[string]bool{
	"acl": true, "amqp": true, "api": true, "ascii": true, "cpu": true, "css": true, "db": true,
	"dns": true, "eof": true, "gid": true, "guid": true, "html": true, "http": true, "https": true,
	"id": true, "ip": true, "json": true, "qps": true, "ram": true, "rpc": true, "rtp": true,
	"sip": true, "sla": true, "smtp": true, "sql": true, "ssh": true, "tcp": true, "tls": true,
	"ts": true, "ttl": true, "udp": true, "ui": true, "uid": true, "uri": true, "url": true,
	"utf": true, "uuid": true, "vm": true, "xml": true, "xmpp": true, "xsrf": true, "xss": true,
	// The protocol's own.
	"acp": true, "fs": true, "llm": true, "mcp": true, "mime": true, "sse": true,
}

// spellings fixes the case of words that are neither initialisms nor
// capitalized like ordinary words.
var spellings = map[string]string{"openai": "OpenAI"}

// Name maps SDK identifiers to exported Go identifiers using [initialisms].
func Name(s string) string {
	var words []string
	var word []rune
	rs := []rune(s)
	flush := func() {
		if len(word) > 0 {
			words = append(words, string(word))
			word = nil
		}
	}
	for i, r := range rs {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()
			continue
		}
		if len(word) > 0 && unicode.IsUpper(r) && (unicode.IsLower(rs[i-1]) || unicode.IsDigit(rs[i-1]) || (i+1 < len(rs) && unicode.IsLower(rs[i+1]) && unicode.IsUpper(rs[i-1]))) {
			flush()
		}
		word = append(word, r)
	}
	flush()
	for i, w := range words {
		lower := strings.ToLower(w)
		// A trailing version number stays attached: utf16 is UTF16.
		letters := strings.TrimRightFunc(lower, unicode.IsDigit)
		if initialisms[letters] {
			words[i] = strings.ToUpper(lower)
			continue
		}
		if spelled, ok := spellings[lower]; ok {
			words[i] = spelled
			continue
		}
		runes := []rune(lower)
		runes[0] = unicode.ToUpper(runes[0])
		words[i] = string(runes)
	}
	result := strings.Join(words, "")
	if result == "" {
		return "Empty"
	}
	if unicode.IsDigit([]rune(result)[0]) {
		result = "Value" + result
	}
	return result
}
