// Package facade generates the typed connection façades for each protocol
// version from a method table plus the parsed schema. The table is the hand
// written half of the design: which wire methods exist is checked against the
// schema, but how they group into Go interfaces and what their docs say is
// decided here.
package facade

import (
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"maps"
	"slices"
	"strings"

	"github.com/ironpark/acp-go/internal/cmd/schema/tsdef"
	"github.com/ironpark/acp-go/internal/cmd/schema/tsgen"
)

// Spec describes one protocol version's façade package.
type Spec struct {
	// Package is the Go package name, Dir its directory relative to the façade
	// root, and SchemaPath the import path of the matching schema package.
	Package    string
	Dir        string
	SchemaPath string
	// Agent lists the methods an agent serves, grouped into interfaces; a client
	// connection gains an outgoing call for each. Client is the mirror image.
	Agent  []Group
	Client []Group
	// Unhandled are wire methods deliberately left to the extension handlers.
	Unhandled []string
	// CapabilityDoc is the prefix to [Group.Capability] that names the
	// capabilities object in initialize, for the generated docs.
	CapabilityDoc string
}

// Group is one Go interface. The group whose Required flag is set becomes the
// Agent or Client interface itself; the others are optional interfaces the
// connection discovers by type assertion.
type Group struct {
	Interface string
	Required  bool
	Doc       string
	Methods   []Method
	// Experimental closes the interface's comment and each outgoing call's
	// with [tsgen.ExperimentalNote].
	Experimental bool
	// Capability is the member of the side's capabilities object that
	// advertises the group, as a dotted path in TypeScript names such as
	// "sessionCapabilities.list". The generated capabilitiesOf sets it for a
	// peer implementing the interface, always for the required group, and the
	// interface's doc says so. NoCapability marks an optional group that no
	// capability advertises; every optional group sets exactly one of the two.
	Capability   string
	NoCapability bool
}

// stability appends the experimental note to s when the group is experimental.
func (g Group) stability(s string) string {
	if !g.Experimental || s == "" {
		return s
	}
	return s + "\n\n" + tsgen.ExperimentalNote
}

// Method is one wire method. A Method without a Response is a notification.
type Method struct {
	Wire     string
	Name     string
	Params   string
	Response string
	// Doc documents the handler on the interface; CallDoc documents the
	// outgoing call on the peer connection and defaults to Doc.
	Doc     string
	CallDoc string
	// Via names a hand-written method on the serving connection, with the
	// handler's signature, that the dispatch calls instead of the interface.
	// It lets the connection observe a method before the handler sees it.
	Via string
	// CallVia names a hand-written method on the calling connection, with the
	// outgoing call's signature, that the call goes through instead of
	// sending directly. It lets the connection fill in or observe the call.
	CallVia string
	// Untimed exempts the outgoing call from the connection's request
	// timeout: it waits for the user, which no timeout can foresee, so only
	// its context bounds it.
	Untimed bool
}

func (m Method) notification() bool { return m.Response == "" }

func (m Method) callDoc() string {
	if m.CallDoc != "" {
		return m.CallDoc
	}
	return m.Doc
}

// connectionMethods are the PROTOCOL_METHODS that internal/jsonrpc handles
// itself, in both directions and for every protocol version, so no method
// table routes them. A protocol method missing here fails generation.
var connectionMethods = []string{"$/cancel_request"}

// side is the per-direction naming the emitter needs.
type side struct {
	constants string // "AGENT_METHODS" or "CLIENT_METHODS"
	groups    []Group
	server    string // connection type serving these methods
	serverVar string // its handler field
	caller    string // connection type calling these methods on the peer
	caps      string // the capabilities type the server advertises
	capsFunc  string // the generated function deriving it
	capsOf    string // the hand-written exported function calling capsFunc
}

// Generate validates spec against schema and returns the façade files.
// schemaFiles and decls are what [tsgen.Generate] produced for the same schema.
func Generate(spec *Spec, schema *tsdef.Schema, schemaFiles tsgen.Files, decls tsgen.Decls) (map[string][]byte, error) {
	g := &emitter{spec: spec, types: map[string]bool{}, constants: map[string]string{}, defs: map[string]*tsdef.Type{}, capSteps: map[string][]capabilityStep{}}
	for _, d := range schema.Types {
		g.types[tsgen.Name(d.Name)] = true
		g.defs[d.Name] = d.Type
	}
	for _, c := range schema.Constants {
		if c.Name != "AGENT_METHODS" && c.Name != "CLIENT_METHODS" && c.Name != "PROTOCOL_METHODS" {
			continue
		}
		for _, m := range c.Members {
			wire := strings.Trim(m.Value, `"`)
			g.constants[c.Name+" "+wire] = tsgen.ConstantName(c.Name, m.Name)
		}
	}
	for key := range g.constants {
		if table, wire, _ := strings.Cut(key, " "); table == "PROTOCOL_METHODS" && !slices.Contains(connectionMethods, wire) {
			return nil, fmt.Errorf("PROTOCOL_METHODS: %s is not handled by the JSON-RPC connection", wire)
		}
	}
	sides := []side{
		{"AGENT_METHODS", spec.Agent, "AgentSideConnection", "agent", "ClientSideConnection", "AgentCapabilities", "capabilitiesOf", "CapabilitiesOf"},
		{"CLIENT_METHODS", spec.Client, "ClientSideConnection", "client", "AgentSideConnection", "ClientCapabilities", "clientCapabilitiesOf", "ClientCapabilitiesOf"},
	}
	for _, s := range sides {
		if err := g.validate(s); err != nil {
			return nil, err
		}
	}

	files := map[string][]byte{}
	if err := g.typesFile(schema, schemaFiles, decls, sides); err != nil {
		return nil, err
	}
	if err := g.flush(files, "types.gen.go"); err != nil {
		return nil, err
	}
	g.methodsFile(sides)
	if err := g.flush(files, "methods.gen.go"); err != nil {
		return nil, err
	}
	return files, nil
}

type emitter struct {
	spec      *Spec
	types     map[string]bool
	constants map[string]string           // "AGENT_METHODS session/load" -> Go constant, for every method table
	defs      map[string]*tsdef.Type      // schema definitions by TypeScript name
	capSteps  map[string][]capabilityStep // Go interface -> its resolved Capability
	out       strings.Builder
}

func (g *emitter) write(f string, a ...any) { fmt.Fprintf(&g.out, f, a...) }

func (g *emitter) flush(files map[string][]byte, name string) error {
	src, err := format.Source([]byte(g.out.String()))
	if err != nil {
		return fmt.Errorf("format %s: %w\n%s", name, err, g.out.String())
	}
	files[name] = src
	g.out.Reset()
	return nil
}

// validate checks the table against the schema so that an upstream change
// fails generation instead of silently leaving a method unrouted.
func (g *emitter) validate(s side) error {
	covered := map[string]bool{}
	names := map[string]bool{}
	required := 0
	for _, group := range s.groups {
		if group.Required {
			required++
		}
		if group.Required && group.NoCapability || !group.Required && (group.Capability != "") == group.NoCapability {
			return fmt.Errorf("%s: %s must set exactly one of Capability and NoCapability, or for the required group at most Capability", s.constants, group.Interface)
		}
		if group.Capability != "" {
			steps, err := g.capabilityPath(s.caps, group.Capability)
			if err != nil {
				return fmt.Errorf("%s: %w", group.Interface, err)
			}
			g.capSteps[group.Interface] = steps
		}
		for _, m := range group.Methods {
			kind := "request"
			if m.notification() {
				kind = "notification"
			}
			key := m.Wire + " " + kind
			if covered[key] {
				return fmt.Errorf("%s: %s listed twice as a %s", s.constants, m.Wire, kind)
			}
			covered[key] = true
			if names[m.Name] {
				return fmt.Errorf("%s: Go method %s used twice", s.constants, m.Name)
			}
			names[m.Name] = true
			if _, ok := g.constants[s.constants+" "+m.Wire]; !ok {
				return fmt.Errorf("%s: %s is not a method constant in this schema", s.constants, m.Wire)
			}
			if !g.types[m.Params] {
				return fmt.Errorf("%s: params type %s not in schema", m.Wire, m.Params)
			}
			if !m.notification() && !g.types[m.Response] {
				return fmt.Errorf("%s: response type %s not in schema", m.Wire, m.Response)
			}
			// The emitter honors one routing per side; reject the rest
			// rather than drop a flag.
			if m.Untimed && (m.notification() || m.CallVia != "") {
				return fmt.Errorf("%s: Untimed applies only to a request sent directly, not a notification or a CallVia", m.Wire)
			}
			if (m.Via != "" || m.CallVia != "") && !group.Required {
				return fmt.Errorf("%s: Via and CallVia apply only to the required group, whose handler always exists", m.Wire)
			}
		}
	}
	if required != 1 {
		return fmt.Errorf("%s: expected exactly one required group, found %d", s.constants, required)
	}
	unhandled := map[string]bool{}
	for _, wire := range g.spec.Unhandled {
		unhandled[wire] = true
	}
	for key := range g.constants {
		prefix, wire, _ := strings.Cut(key, " ")
		if prefix != s.constants {
			continue
		}
		if !covered[wire+" request"] && !covered[wire+" notification"] && !unhandled[wire] {
			return fmt.Errorf("%s: %s is neither in the method table nor listed as unhandled", s.constants, wire)
		}
	}
	return nil
}

func (g *emitter) header() {
	g.write("// Code generated by acp-schema from the method table; DO NOT EDIT.\n\npackage %s\n\n", g.spec.Package)
}

// typesFile re-exports the payload types, and every schema type they reach,
// so a caller implementing the interfaces needs one import.
func (g *emitter) typesFile(schema *tsdef.Schema, schemaFiles tsgen.Files, decls tsgen.Decls, sides []side) error {
	var payloads, roots []string
	for _, d := range schema.Types {
		if name := tsgen.Name(d.Name); payloadType(name) {
			payloads = append(payloads, name)
		}
	}
	roots = append(roots, payloads...)
	for _, s := range sides {
		for _, group := range s.groups {
			for _, m := range group.Methods {
				roots = append(roots, m.Params, m.Response)
			}
		}
	}
	reached, err := reachable(schemaFiles, decls, roots)
	if err != nil {
		return err
	}
	g.header()
	g.write("import schema %q\n\n", g.spec.SchemaPath)
	g.write("// ProtocolVersion is the ACP protocol version implemented by this package.\nconst ProtocolVersion = schema.CurrentProtocolVersion\n\n")
	g.write("// Every request, response and notification payload, re-exported so that a\n// caller implementing [Agent] or [Client] needs one import.\n\ntype (\n")
	for _, name := range payloads {
		g.write("\t%s = schema.%s\n", name, name)
	}
	g.write(")\n\n// Every type those payloads reach, with the constants and constructors\n// that come with them.\n\ntype (\n")
	var constants, constructors []string
	for _, name := range reached {
		if name == "ProtocolVersion" {
			// The façade's own ProtocolVersion is the version constant; the
			// type stays in the schema package.
			continue
		}
		if !slices.Contains(payloads, name) {
			g.write("\t%s = schema.%s\n", name, name)
		}
		d := decls[name]
		constants = append(constants, d.Constants...)
		if d.Constructor != "" {
			constructors = append(constructors, name)
		}
	}
	g.write(")\n")
	if len(constants) > 0 {
		g.write("\nconst (\n")
		for _, c := range constants {
			g.write("\t%s = schema.%s\n", c, c)
		}
		g.write(")\n")
	}
	for _, t := range constructors {
		d := decls[t]
		if d.Interface != "" {
			g.write("\n// %s wraps a variant.\n", d.Constructor)
			g.write("func %s[T %s](v T) %s { return schema.%s(v) }\n", d.Constructor, d.Constraint, t, d.Constructor)
			continue
		}
		g.write("\n// %s encodes value, one of the %s types.\n", d.Constructor, d.Constraint)
		g.write("func %s[T %s](value T) (%s, error) { return schema.%s(value) }\n", d.Constructor, d.Constraint, t, d.Constructor)
	}
	return nil
}

// reachable returns, sorted, the exported types of the generated schema
// package that roots name or refer to through other types. A union reaches
// its variants and the constraint its constructor takes.
func reachable(schemaFiles tsgen.Files, decls tsgen.Decls, roots []string) ([]string, error) {
	deps := map[string][]string{}
	fset := token.NewFileSet()
	for _, name := range slices.Sorted(maps.Keys(schemaFiles)) {
		f, err := parser.ParseFile(fset, name, schemaFiles[name], parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts := spec.(*ast.TypeSpec)
				if ts.Name.IsExported() {
					deps[ts.Name.Name] = typeRefs(ts.Type)
				}
			}
		}
	}
	for name, d := range decls {
		deps[name] = append(deps[name], d.Variants...)
		for _, extra := range []string{d.Interface, d.Constraint} {
			if extra != "" {
				deps[name] = append(deps[name], extra)
			}
		}
	}
	seen := map[string]bool{}
	var visit func(string)
	visit = func(name string) {
		if _, declared := deps[name]; !declared || seen[name] {
			return
		}
		seen[name] = true
		for _, dep := range deps[name] {
			visit(dep)
		}
	}
	for _, root := range roots {
		visit(root)
	}
	return slices.Sorted(maps.Keys(seen)), nil
}

// typeRefs returns the identifiers a type expression uses, leaving out field
// and method names and qualified identifiers from other packages.
func typeRefs(expr ast.Expr) []string {
	var refs []string
	ast.Inspect(expr, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Field:
			refs = append(refs, typeRefs(n.Type)...)
			return false
		case *ast.SelectorExpr:
			return false
		case *ast.Ident:
			refs = append(refs, n.Name)
		}
		return true
	})
	return refs
}

// payloadType reports whether a schema type is a method payload rather than
// part of the JSON-RPC envelope.
func payloadType(name string) bool {
	if tsgen.Envelope(name) {
		return false
	}
	for _, suffix := range []string{"Request", "Response", "Notification"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

func doc(s string) string {
	if s == "" {
		return ""
	}
	return "// " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n// ") + "\n"
}

// methodsFile emits the interfaces, the outgoing calls and the dispatch
// switches for both directions.
func (g *emitter) methodsFile(sides []side) {
	g.header()
	g.write("// The hand-written half of each façade provides AgentSideConnection and\n// ClientSideConnection with a conn *jsonrpc.Connection field and an agent or\n// client field holding the served interface; the lifecycle and extension\n// methods every connection shares, and everything routed by method name, are\n// generated here.\n\n")
	g.write("import (\n\t\"context\"\n\t\"encoding/json/jsontext\"\n\n\tacp \"github.com/ironpark/acp-go\"\n\t\"github.com/ironpark/acp-go/internal/acpconn\"\n\t\"github.com/ironpark/acp-go/internal/jsonrpc\"\n\tschema %q\n)\n\n", g.spec.SchemaPath)
	for _, s := range sides {
		g.interfaces(s)
	}
	for _, s := range sides {
		g.connection(s.server)
	}
	for _, s := range sides {
		g.outgoing(s)
	}
	for _, s := range sides {
		g.dispatch(s)
	}
	for _, s := range sides {
		g.capabilities(s)
	}
}

// groupDoc is the group's doc with a sentence naming its capability, placed
// before the protocol docs link when there is one.
func (g *emitter) groupDoc(s side, group Group) string {
	if group.Capability == "" {
		return group.Doc
	}
	party := strings.ToLower(strings.TrimSuffix(s.caps, "Capabilities"))
	sentence := fmt.Sprintf("[%s] advertises it with the `%s%s` %s capability.", s.capsOf, g.spec.CapabilityDoc, group.Capability, party)
	text, link, linked := strings.Cut(group.Doc, "\n\nSee protocol docs")
	if linked {
		link = "\n\nSee protocol docs" + link
	}
	return text + "\n\n" + sentence + link
}

// capabilityStep is one member on a capability path: its Go field, and the
// Go type of the object it holds, or "" for a boolean.
type capabilityStep struct{ field, object string }

// capabilityPath resolves a dotted TypeScript member path from the root
// capabilities type. Every member is optional; all but the last hold an
// object, and the last an object or a boolean.
func (g *emitter) capabilityPath(root, path string) ([]capabilityStep, error) {
	t := g.defs[root]
	var steps []capabilityStep
	members := strings.Split(path, ".")
	for i, member := range members {
		if t == nil || t.Kind != tsdef.KindObject {
			return nil, fmt.Errorf("capability %s: %s is not an object", path, strings.Join(members[:i], "."))
		}
		var field *tsdef.Field
		for j := range t.Fields {
			if t.Fields[j].Name == member {
				field = &t.Fields[j]
			}
		}
		if field == nil || !field.Optional {
			return nil, fmt.Errorf("capability %s: no optional member %s", path, member)
		}
		value, _ := field.Type.NonNull()
		step := capabilityStep{field: tsgen.Name(member)}
		switch {
		case value.Kind == tsdef.KindBoolean && i == len(members)-1:
		case value.Kind == tsdef.KindRef && g.defs[value.Name] != nil && g.defs[value.Name].Kind == tsdef.KindObject:
			step.object = tsgen.Name(value.Name)
			t = g.defs[value.Name]
		default:
			return nil, fmt.Errorf("capability %s: %s is neither an object nor a final boolean", path, member)
		}
		steps = append(steps, step)
	}
	return steps, nil
}

// capabilities emits the function that sets, for each optional interface the
// peer implements, the capability advertising it, creating the objects on
// its path as needed.
func (g *emitter) capabilities(s side) {
	required := ""
	for _, group := range s.groups {
		if group.Required {
			required = group.Interface
		}
	}
	g.write("// %s sets in caps the capability of the %s interface and of each\n// optional interface %s implements.\n", s.capsFunc, required, s.serverVar)
	g.write("func %s(%s %s, caps *schema.%s) {\n", s.capsFunc, s.serverVar, required, s.caps)
	// The required group goes first: the objects it always sets need no nil
	// check after it.
	groups := slices.Clone(s.groups)
	slices.SortStableFunc(groups, func(a, b Group) int {
		if a.Required == b.Required {
			return 0
		}
		if a.Required {
			return -1
		}
		return 1
	})
	set := map[string]bool{}
	for _, group := range groups {
		steps := g.capSteps[group.Interface]
		if steps == nil {
			continue
		}
		if !group.Required {
			g.write("\tif _, ok := %s.(%s); ok {\n", s.serverVar, group.Interface)
		}
		at := "caps"
		for i, step := range steps {
			at += "." + step.field
			switch {
			case step.object == "":
				g.write("\t\t%s = new(true)\n", at)
			case i == len(steps)-1:
				g.write("\t\t%s = &schema.%s{}\n", at, step.object)
			case !set[at]:
				g.write("\t\tif %s == nil {\n\t\t\t%s = &schema.%s{}\n\t\t}\n", at, at, step.object)
			}
			if group.Required {
				set[at] = true
			}
		}
		if !group.Required {
			g.write("\t}\n")
		}
	}
	g.write("}\n\n")
}

// connection emits the [acp.Conn] methods, which every connection type
// forwards to its JSON-RPC connection alike.
func (g *emitter) connection(typ string) {
	g.write("var _ acp.Conn = (*%s)(nil)\n\n", typ)
	g.write("// Start processes messages until the peer disconnects or ctx is cancelled.\nfunc (c *%s) Start(ctx context.Context) error { return c.conn.Start(ctx) }\n\n", typ)
	g.write("// Close shuts the connection down, waiting for in-flight handlers.\nfunc (c *%s) Close() error { return c.conn.Close() }\n\n", typ)
	g.write("// Done is closed once the connection stops.\nfunc (c *%s) Done() <-chan struct{} { return c.conn.Done() }\n\n", typ)
	g.write("// ExtMethod sends a request outside the spec and returns its raw result.\nfunc (c *%s) ExtMethod(ctx context.Context, method string, params any) (jsontext.Value, error) {\n\treturn c.conn.SendRequest(ctx, method, params)\n}\n\n", typ)
	g.write("// ExtNotification sends a notification outside the spec.\nfunc (c *%s) ExtNotification(ctx context.Context, method string, params any) error {\n\treturn c.conn.SendNotification(ctx, method, params)\n}\n\n", typ)
}

func (g *emitter) interfaces(s side) {
	for _, group := range s.groups {
		g.write("%stype %s interface {\n", doc(group.stability(g.groupDoc(s, group))), group.Interface)
		for i, m := range group.Methods {
			if i > 0 {
				g.write("\n")
			}
			g.write("%s\t%s\n", indentDoc(m.Doc), signature(m))
		}
		g.write("}\n\n")
	}
}

func indentDoc(s string) string {
	if s == "" {
		return ""
	}
	return "\t" + strings.ReplaceAll(doc(s), "\n// ", "\n\t// ")
}

func signature(m Method) string {
	if m.notification() {
		return fmt.Sprintf("%s(ctx context.Context, params *%s) error", m.Name, m.Params)
	}
	return fmt.Sprintf("%s(ctx context.Context, params *%s) (*%s, error)", m.Name, m.Params, m.Response)
}

// outgoing emits the caller connection's methods for reaching the peer.
func (g *emitter) outgoing(s side) {
	g.write("// --- Outgoing calls from %s to the peer ---\n\n", s.caller)
	for _, group := range s.groups {
		for _, m := range group.Methods {
			constant := g.constants[s.constants+" "+m.Wire]
			g.write("%sfunc (c *%s) %s {\n", doc(group.stability(m.callDoc())), s.caller, signature(m))
			switch {
			case m.CallVia != "":
				g.write("\treturn c.%s(ctx, params)\n}\n\n", m.CallVia)
			case m.notification():
				g.write("\treturn c.conn.SendNotification(ctx, schema.%s, params)\n}\n\n", constant)
			case m.Untimed:
				g.write("\treturn acpconn.CallUntimed[%s](ctx, c.conn, schema.%s, params)\n}\n\n", m.Response, constant)
			default:
				g.write("\treturn acpconn.Call[%s](ctx, c.conn, schema.%s, params)\n}\n\n", m.Response, constant)
			}
		}
	}
}

// dispatch emits the server connection's request and notification routing.
func (g *emitter) dispatch(s side) {
	for _, notification := range []bool{false, true} {
		if notification {
			g.write("func (c *%s) handleNotification(ctx context.Context, method string, params jsontext.Value) error {\n", s.server)
		} else {
			g.write("func (c *%s) handleRequest(ctx context.Context, method string, params jsontext.Value) (any, error) {\n", s.server)
		}
		g.write("\tswitch method {\n")
		for _, group := range s.groups {
			for _, m := range group.Methods {
				if m.notification() != notification {
					continue
				}
				constant := g.constants[s.constants+" "+m.Wire]
				helper, ret := "acpconn.Notify", "return "
				if !notification {
					helper = "acpconn.Request"
				}
				g.write("\tcase schema.%s:\n", constant)
				if m.Via != "" {
					g.write("\t\t%s%s(ctx, schema.Validated(), params, c.%s)\n", ret, helper, m.Via)
				} else if group.Required {
					g.write("\t\t%s%s(ctx, schema.Validated(), params, c.%s.%s)\n", ret, helper, s.serverVar, m.Name)
				} else {
					g.write("\t\tif h, ok := c.%s.(%s); ok {\n\t\t\t%s%s(ctx, schema.Validated(), params, h.%s)\n\t\t}\n", s.serverVar, group.Interface, ret, helper, m.Name)
				}
			}
		}
		if notification {
			g.write("\tdefault:\n\t\tif h, ok := c.%s.(ExtNotificationHandler); ok {\n\t\t\treturn h.ServeExtNotification(ctx, method, params)\n\t\t}\n\t}\n\treturn jsonrpc.MethodNotFound(method)\n}\n\n", s.serverVar)
		} else {
			g.write("\tdefault:\n\t\tif h, ok := c.%s.(ExtMethodHandler); ok {\n\t\t\treturn h.ServeExtMethod(ctx, method, params)\n\t\t}\n\t}\n\treturn nil, jsonrpc.MethodNotFound(method)\n}\n\n", s.serverVar)
		}
	}
}
