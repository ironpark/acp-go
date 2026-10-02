package facade

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ironpark/acp-go/internal/cmd/schema/tsdef"
	"github.com/ironpark/acp-go/internal/cmd/schema/tsgen"
)

const fixture = `
export type PingRequest = { sessionId: string; marker?: Marker; };
export type PingResponse = { ok: boolean; };
export type ByeNotification = { reason?: string; };
export type Marker = string;
export type Unused = { note: string; };
export type AgentCapabilities = { session?: SessionCapabilities | null; flag?: boolean; name: string; };
export type SessionCapabilities = { bye?: ByeCapabilities | null; };
export type ByeCapabilities = { _meta?: { [key: string]: unknown } | null; };
export type ClientCapabilities = { flag?: boolean; };
export const AGENT_METHODS = { ping: "ping", session_bye: "session/bye", extra: "extra/one" } as const;
export const CLIENT_METHODS = { notice: "notice" } as const;
export const PROTOCOL_METHODS = { cancel_request: "$/cancel_request" } as const;
export const PROTOCOL_VERSION = 1;
`

func parse(t *testing.T) *tsdef.Schema {
	t.Helper()
	s, err := tsdef.Parse("fixture.ts", []byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// generate runs Generate with the declarations tsgen reports for schema.
func generate(t *testing.T, s *Spec, schema *tsdef.Schema) (map[string][]byte, error) {
	t.Helper()
	files, decls, err := tsgen.Generate(schema, "schema")
	if err != nil {
		t.Fatal(err)
	}
	return Generate(s, schema, files, decls)
}

func spec() *Spec {
	return &Spec{
		Package: "fixture", SchemaPath: "example.com/schema", Unhandled: []string{"extra/one"},
		Agent: []Group{
			{Interface: "Agent", Required: true, Doc: "Agent doc.", Methods: []Method{
				{Wire: "ping", Name: "Ping", Params: "PingRequest", Response: "PingResponse", Doc: "Ping doc."},
			}},
			{Interface: "Byer", Capability: "session.bye", Doc: "Byer doc.\n\nSee protocol docs: [Bye](https://example.com)", Methods: []Method{
				{Wire: "session/bye", Name: "Bye", Params: "ByeNotification", CallDoc: "Bye call."},
			}},
		},
		Client: []Group{
			{Interface: "Client", Required: true, Methods: []Method{
				{Wire: "notice", Name: "Notice", Params: "ByeNotification"},
			}},
		},
	}
}

func TestGenerateEmitsInterfacesCallsAndDispatch(t *testing.T) {
	files, err := generate(t, spec(), parse(t))
	if err != nil {
		t.Fatal(err)
	}
	methods := string(files["methods.gen.go"])
	for _, want := range []string{
		"type Agent interface {",
		"Ping(ctx context.Context, params *PingRequest) (*PingResponse, error)",
		"type Byer interface {",
		"func (c *ClientSideConnection) Ping(",
		"acpconn.Call[PingResponse](ctx, c.conn, schema.AgentMethodsPing, params)",
		"func (c *ClientSideConnection) Bye(",
		"c.conn.SendNotification(ctx, schema.AgentMethodsSessionBye, params)",
		"case schema.AgentMethodsPing:\n\t\treturn acpconn.Request(ctx, schema.Validated(), params, c.agent.Ping)",
		"if h, ok := c.agent.(Byer); ok {\n\t\t\treturn acpconn.Notify(ctx, schema.Validated(), params, h.Bye)",
		"func (c *AgentSideConnection) handleRequest(",
		"func (c *ClientSideConnection) handleNotification(",
		"// Bye call.\nfunc (c *ClientSideConnection) Bye(",
	} {
		if !strings.Contains(methods, want) {
			t.Errorf("methods.gen.go lacks %q\n%s", want, methods)
		}
	}
	// gofmt aligns the alias block, so compare with whitespace collapsed.
	types := strings.Join(strings.Fields(string(files["types.gen.go"])), " ")
	for _, want := range []string{"PingRequest = schema.PingRequest", "ByeNotification = schema.ByeNotification", "Marker = schema.Marker", "const ProtocolVersion = schema.CurrentProtocolVersion"} {
		if !strings.Contains(types, want) {
			t.Errorf("types.gen.go lacks %q\n%s", want, types)
		}
	}
	// Marker is reached through PingRequest; Unused is reached by nothing.
	if strings.Contains(types, "Unused") {
		t.Errorf("types.gen.go re-exports a type no payload reaches\n%s", types)
	}
	// The extension method must not be routed and the protocol method never appears.
	for _, unwanted := range []string{"ExtraOne", "CancelRequest"} {
		if strings.Contains(methods, unwanted) {
			t.Errorf("methods.gen.go routes %s", unwanted)
		}
	}
}

func TestCallViaRoutesTheOutgoingCall(t *testing.T) {
	s := spec()
	s.Agent[0].Methods[0].CallVia = "ping"
	files, err := generate(t, s, parse(t))
	if err != nil {
		t.Fatal(err)
	}
	methods := string(files["methods.gen.go"])
	if want := "(*PingResponse, error) {\n\treturn c.ping(ctx, params)\n}"; !strings.Contains(methods, want) {
		t.Errorf("methods.gen.go lacks %q\n%s", want, methods)
	}
	// The dispatch still reaches the handler.
	if want := "acpconn.Request(ctx, schema.Validated(), params, c.agent.Ping)"; !strings.Contains(methods, want) {
		t.Errorf("methods.gen.go lacks %q", want)
	}
}

func TestValidationRejectsDrift(t *testing.T) {
	cases := map[string]func(*Spec){
		"uncovered method constant": func(s *Spec) { s.Unhandled = nil },
		"unknown wire method":       func(s *Spec) { s.Agent[1].Methods[0].Wire = "session/gone" },
		"unknown params type":       func(s *Spec) { s.Agent[0].Methods[0].Params = "Nope" },
		"unknown response type":     func(s *Spec) { s.Agent[0].Methods[0].Response = "Nope" },
		"duplicate method kind": func(s *Spec) {
			s.Agent[1].Methods = append(s.Agent[1].Methods, s.Agent[1].Methods[0])
		},
		"duplicate Go name": func(s *Spec) {
			s.Agent[1].Methods = append(s.Agent[1].Methods, Method{Wire: "ping", Name: "Ping", Params: "ByeNotification"})
		},
		"no required group":                 func(s *Spec) { s.Agent[0].Required = false },
		"capability on the required group":  func(s *Spec) { s.Agent[0].NoCapability = true },
		"optional group without capability": func(s *Spec) { s.Agent[1].Capability = "" },
		"capability and NoCapability":       func(s *Spec) { s.Agent[1].NoCapability = true },
		"unknown capability member":         func(s *Spec) { s.Agent[1].Capability = "session.gone" },
		"capability through a boolean":      func(s *Spec) { s.Agent[1].Capability = "flag.bye" },
		"required capability member":        func(s *Spec) { s.Agent[1].Capability = "name" },
		"untimed notification":              func(s *Spec) { s.Agent[1].Methods[0].Untimed = true },
		"untimed CallVia": func(s *Spec) {
			s.Agent[0].Methods[0].Untimed, s.Agent[0].Methods[0].CallVia = true, "ping"
		},
		"Via outside the required group":     func(s *Spec) { s.Agent[1].Methods[0].Via = "bye" },
		"CallVia outside the required group": func(s *Spec) { s.Agent[1].Methods[0].CallVia = "bye" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := spec()
			mutate(s)
			if _, err := generate(t, s, parse(t)); err == nil {
				t.Fatal("generation accepted the drift")
			}
		})
	}
}

func TestValidationRejectsUnhandledProtocolMethod(t *testing.T) {
	source := strings.Replace(fixture, `cancel_request: "$/cancel_request"`, `cancel_request: "$/cancel_request", ping: "$/ping"`, 1)
	schema, err := tsdef.Parse("fixture.ts", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := generate(t, spec(), schema); err == nil || !strings.Contains(err.Error(), "$/ping") {
		t.Fatalf("generation accepted a protocol method nothing handles: %v", err)
	}
}

func TestSameWireMethodMayBeRequestAndNotification(t *testing.T) {
	s := spec()
	s.Agent[1].Methods = append(s.Agent[1].Methods, Method{Wire: "session/bye", Name: "ByeRequest", Params: "PingRequest", Response: "PingResponse"})
	if _, err := generate(t, s, parse(t)); err != nil {
		t.Fatal(err)
	}
}

func TestPinnedTablesMatchPinnedSchemas(t *testing.T) {
	for version, s := range map[string]*Spec{"v1": V1, "v2": V2} {
		schema, err := tsdef.ParseDir("../../../../schema/typescript/" + version)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := generate(t, s, schema); err != nil {
			t.Fatalf("%s: %v", version, err)
		}
	}
}

// TestTableNamesFollowGoNaming keeps the hand-written names in the method
// tables in line with the generator's initialisms, so a façade method never
// spells a word differently from the types it takes.
func TestTableNamesFollowGoNaming(t *testing.T) {
	for version, s := range map[string]*Spec{"v1": V1, "v2": V2} {
		for _, groups := range [][]Group{s.Agent, s.Client} {
			for _, g := range groups {
				if g.Interface != "" && tsgen.Name(g.Interface) != g.Interface {
					t.Errorf("%s: interface %s should be %s", version, g.Interface, tsgen.Name(g.Interface))
				}
				for _, m := range g.Methods {
					if tsgen.Name(m.Name) != m.Name {
						t.Errorf("%s: method %s should be %s", version, m.Name, tsgen.Name(m.Name))
					}
				}
			}
		}
	}
}

// TestHandWrittenDocsNameSchemaTypes keeps the façades' doc comments in step
// with the schema: a schema.X they mention must exist, and must be written
// without the schema qualifier when the façade re-exports it.
func TestHandWrittenDocsNameSchemaTypes(t *testing.T) {
	ref := regexp.MustCompile(`schema\.([A-Z]\w*)`)
	for version, s := range map[string]*Spec{"v1": V1, "v2": V2} {
		root := "../../../../"
		generated, err := filepath.Glob(root + "schema/" + version + "/*.gen.go")
		if err != nil {
			t.Fatal(err)
		}
		declared := topLevelNames(t, generated...)
		reexported := topLevelNames(t, root+s.Dir+"/types.gen.go")
		files, err := filepath.Glob(root + s.Dir + "/*.go")
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range files {
			if strings.HasSuffix(path, ".gen.go") || strings.HasSuffix(path, "_test.go") {
				continue
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for i, line := range strings.Split(string(data), "\n") {
				if !strings.HasPrefix(strings.TrimSpace(line), "//") {
					continue
				}
				for _, m := range ref.FindAllStringSubmatch(line, -1) {
					switch name := m[1]; {
					case reexported[name]:
						t.Errorf("%s:%d: %s is re-exported; drop the schema qualifier", path, i+1, name)
					case !declared[name]:
						t.Errorf("%s:%d: schema.%s does not exist in %s", path, i+1, name, version)
					}
				}
			}
		}
	}
}

// topLevelNames returns the package-level names the Go files declare:
// types, constants, variables and functions other than methods.
func topLevelNames(t *testing.T, paths ...string) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	fset := token.NewFileSet()
	for _, path := range paths {
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			switch decl := decl.(type) {
			case *ast.FuncDecl:
				if decl.Recv == nil {
					names[decl.Name.Name] = true
				}
			case *ast.GenDecl:
				for _, spec := range decl.Specs {
					switch spec := spec.(type) {
					case *ast.TypeSpec:
						names[spec.Name.Name] = true
					case *ast.ValueSpec:
						for _, n := range spec.Names {
							names[n.Name] = true
						}
					}
				}
			}
		}
	}
	return names
}

func TestCapabilitiesOfSetsEachPath(t *testing.T) {
	files, err := generate(t, spec(), parse(t))
	if err != nil {
		t.Fatal(err)
	}
	methods := string(files["methods.gen.go"])
	want := `func capabilitiesOf(agent Agent, caps *schema.AgentCapabilities) {
	if _, ok := agent.(Byer); ok {
		if caps.Session == nil {
			caps.Session = &schema.SessionCapabilities{}
		}
		caps.Session.Bye = &schema.ByeCapabilities{}
	}
}`
	if !strings.Contains(methods, want) {
		t.Errorf("methods.gen.go lacks %q\n%s", want, methods)
	}
	if want := "// Byer doc.\n//\n// [CapabilitiesOf] advertises it with the `session.bye` agent capability.\n//\n// See protocol docs:"; !strings.Contains(methods, want) {
		t.Errorf("methods.gen.go lacks %q\n%s", want, methods)
	}
	if !strings.Contains(methods, "func clientCapabilitiesOf(client Client, caps *schema.ClientCapabilities) {\n}") {
		t.Errorf("methods.gen.go lacks an empty clientCapabilitiesOf\n%s", methods)
	}
}

func TestRequiredCapabilityIsAlwaysSet(t *testing.T) {
	s := spec()
	s.Agent[0].Capability = "session"
	files, err := generate(t, s, parse(t))
	if err != nil {
		t.Fatal(err)
	}
	// Set unconditionally and first, so the optional group needs no nil check.
	want := "caps.Session = &schema.SessionCapabilities{}\n\tif _, ok := agent.(Byer); ok {\n\t\tcaps.Session.Bye = &schema.ByeCapabilities{}\n\t}"
	if methods := string(files["methods.gen.go"]); !strings.Contains(methods, want) {
		t.Errorf("methods.gen.go lacks %q\n%s", want, methods)
	}
}
