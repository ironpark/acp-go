# TypeScript schema generation

The generator reads the official [TypeScript SDK](https://github.com/agentclientprotocol/typescript-sdk)
using [go-tree-sitter](https://github.com/tree-sitter/go-tree-sitter) and the TypeScript grammar.
`typescript/REVISION` pins the upstream commit; its license is in `typescript/LICENSE`.
Generation uses checked-in sources and requires no Node.js or network access.
Go 1.27 or newer is required by both modules. CGO and a C compiler are required for the
generator, but not for generated packages. Generated code uses `encoding/json/v2`,
`encoding/json/jsontext`; no `GOEXPERIMENT` setting is needed.
Each version produces the wire types split by kind — `methods.gen.go` (method constants and the
protocol version), `enums.gen.go` (identifier types and literal enums), `types.gen.go` (object
structs and aliases), `unions.gen.go` (tagged and raw payload unions) and
`envelope.gen.go` (the JSON-RPC envelope: `AgentRequest`, `ClientResponse`, `RequestID`, `Error` …),
`getters.gen.go` (nil-safe `GetX` methods for the pointer fields of payload structs) —
and `zod.gen.go` (Zod rule tables, the `Validated()` options and generic `Decode`/`Validate`).
The split is by declaration kind, not by domain, so it needs no mapping table that could drift. The rule evaluator lives once in `schema/internal/zod` and the
union runtime (alternative matching for raw unions, tag splicing for tagged unions) once in
`schema/internal/union`; both are shared by the versions as runtime dependencies of the generated
packages, internal so that only the schema packages import them. Every `_meta` member is generated as `Meta`, an alias of the public
`schema/meta.Meta` map, which keeps values as raw JSON and adds `Of`, `Set` and `Get[T]`.

```sh
# From the repository root:
go generate ./...
go test ./...
(cd internal/cmd/schema && go test ./...)
(cd internal/cmd/schema && go run . -source ../../../schema/typescript -out ../../../schema -facade ../../.. -check)

# Refresh intentionally, using a full upstream commit SHA:
./schema/update.sh <40-character-commit-sha>
go generate ./...
```

After a refresh, update the schema versions in the README tables (`README.md`,
`docs/README.ko.md`) to the releases the new snapshot was generated from.

Inputs and outputs:

| Upstream | Snapshot | Go import |
| --- | --- | --- |
| `src/schema/*.ts` | `typescript/v1` | `github.com/ironpark/acp-go/schema/v1` |
| `src/v2/schema/*.ts` | `typescript/v2` | `github.com/ironpark/acp-go/schema/v2` |

Both Go packages are named `schema`; use aliases such as `acp1` and `acp2` when importing both.
`acp1` implements ACP v1 on top of `schema/v1`, `acp2` implements the draft v2 on top of
`schema/v2`, the root `acp` package holds the runtime they share, and `router` serves both on one
endpoint. The previous JSON Schema generator,
inputs and configuration have been removed.

## Façade generation

`-facade <module root>` also writes `types.gen.go` and `methods.gen.go` into `acp1` and `acp2`. Their input is the method table in `internal/cmd/schema/facade/{v1,v2}.go`: how
wire methods group into Go interfaces, which are required, and what their docs say. The generator
checks the table against the schema constants and type names, so an upstream method that is
neither in the table nor listed as `Unhandled` fails generation instead of silently going unrouted.
The generated files hold the interfaces, the outgoing calls on both connection types, the dispatch
switches and the type aliases; the hand-written files keep the connection structs, constructors,
lifecycle methods and the extension hooks.

## Supported subset

- `types.gen.ts`: exported aliases, references, primitives, literal enums, objects,
  optional properties, nullable types, arrays, string index signatures, unions and object intersections.
- `index.ts`: protocol version and method constants. Imports, re-exports and private declarations
  are ignored. Unsupported exported declarations fail with a source location.
- `zod.gen.ts`: schema references, primitives, scalar literals, objects, records, arrays,
  unions/intersections, optional/nullable/nullish wrappers, defaults, integer/numeric/length
  bounds, regular expressions, URL and ISO datetime builders. The ACP helpers
  `defaultOnError`, `requiredDefaultOnError`, `vecSkipError`, `excludeKnownTags` and
  `preserveCustomPayload` retain their ordering and distinct semantics. Unsupported builder
  expressions fail with a source location; no JavaScript code is evaluated.
- `guards.gen.ts`: retained with the upstream snapshot for reference; not executed or translated.

Optional scalar fields use pointers with `omitzero`, retaining explicit false, zero and empty strings.
Optional slices and maps are plain values: nil is omitted and an empty non-nil value encodes as `[]` or `{}`.
Nullable fields use pointers; optional null and absence share the nil representation, except
the members `overrides.yaml` lists as tristate (the protocol's Rust schema declares them
`MaybeUndefined`), where null clears a value an absent member keeps: those are
`optional.Value[T]` from `schema/optional`, which tells absent, null and a value apart.
A tagged union without a default variant rejects an object that lacks its discriminator, with
or without validation; an unknown discriminator still decodes as the Unknown or catch-all variant.
Literal unions that also admit the underlying primitive, such as `"a" | "b" | string`, produce a
named scalar type with constants and a `Known` method.
A number whose Zod rule is an integer gets the narrowest of `uint16`, `uint32`, `uint64`, `int32` and
`int64` its bounds allow; an integer rule the generator cannot pair with a number fails generation.
Other TypeScript numbers use `float64`, except those `internal/cmd/schema/overrides.yaml`
gives an integer type because the protocol's Rust schema declares them integers; unknown payloads use `jsontext.Value` to preserve
large numbers and extension data. Object index signatures use JSON v2's `embed` fallback:
additional properties retain their declared value type, and duplicate keys that collide with
named fields are rejected. Caller options such as deterministic map ordering propagate normally.

JSON v2 defaults reject duplicate object members and invalid UTF-8, and match field names
case-sensitively. Required nil slices/maps encode as empty arrays/objects. Nullable pointers
still encode nil as null. The root runtime uses `encoding/json/v2` as well, so these defaults
apply end to end.

Literal unions produce named scalar types and constants.

Discriminated object unions such as `SessionUpdate`, `ContentBlock` and `McpServer` become a small
wrapper struct around a sealed `<Type>Variant` interface. Each variant is a plain struct without
the discriminator member; the tag is implied by the Go type, written first by the variant's own
`MarshalJSONTo`, and checked by its `UnmarshalJSONFrom`. Decode with ordinary `json.Unmarshal`
and branch with a type switch:

```go
var update acp2.SessionUpdate
if err := json.Unmarshal(data, &update); err != nil { ... }
switch v := update.Variant().(type) {
case acp2.SessionUpdateAgentMessageChunk:
	// v.Content ...
case acp2.SessionUpdateCustom:
	// v.SessionUpdate holds the unknown tag; v.AdditionalProperties keeps every member.
}
out := acp2.NewSessionUpdate(acp2.SessionUpdateAgentMessageChunk{Content: block})
```

A catch-all `{ tag: string; [key: string]: unknown }` member becomes `<Type>Custom`, so unknown
tags round-trip unchanged including large numbers. A tagged union without one gets a generated
`<Type>Unknown{Raw jsontext.Value}` variant instead: unknown tags decode into it and `Raw` is
encoded unchanged, so a peer on a newer schema never fails the whole message. Its `Validated`
rule is wrapped in a Go-only `OpenTags` rule that lets unknown tags through while known tags are
still validated in full; this is deliberately more lenient than the TypeScript SDK. A catch-all
that is not a single object (`(A | B) & { tag: string; … }`) is also represented by `Unknown`.
Members of the form `Inner & { tag: "x" }` where `Inner` is, or expands to, a union (for example
`StateUpdate` inside `SessionUpdate`) become `struct { Value Inner }`; the outer tag is spliced
into the inner object on encode and removed on decode.

One member may leave the tag out entirely, as v1 `McpServer`'s stdio transport does. It is the
default variant: a payload without the tag decodes into it, it encodes without one, and, as in the
SDK, a payload with a tag this SDK does not know falls back to it too (such unions need no
`Unknown` variant). When that member is a schema type nothing else refers to (`MCPServerStdio`),
the type itself is the variant.

A member `Base & { tag: "x" }` whose `Base` nothing else refers to takes `Base` over: `Base` is not
declared on its own, and the variant, under its usual name, keeps `Base`'s comment and gets a Zod
rule that is `Base`'s rule plus the tag, so `Decode`/`Validate` work on it. The variant's name is
often `Base`'s own (`MCPServer` + `"http"` = `MCPServerHTTP`); otherwise the duplicate goes away
(v2 `IdleStateUpdate` is `StateUpdateIdle`, v1 `TextContent` is `ContentBlockText`).

Missing required members are not rejected by plain decoding; use `Validated` for
SDK-level validation. The zero wrapper encodes as `null`, `null` decodes to the zero wrapper, and
wrappers implement `IsZero`, so optional union fields are plain values omitted when unset.
Callers that prefer interface-typed fields can declare `<Type>Variant` fields directly and decode
with `json.WithUnmarshalers(acp2.Unmarshalers())`; encoding needs no options.

Unions that are not discriminated objects (`RequestId`, `AgentResponse`, `ElicitationContentValue`,
method `params` unions, ...) preserve their JSON payload and expose a generic method `As[T]`, a
generic constructor `New<Union>[T]`, plus `RawJSON`. Both are constrained by the
generated type set `<Union>Alternative`, so asking for a type the union cannot hold is a compile
error (generic methods require Go 1.27):

```go
req, err := msg.Params.As[acp1.PromptRequest]()          // (T, error)
id, err := schemav1.NewRequestID("abc")                     // string | float64 | jsontext.Value
```

Alternatives that name the same Go type (`ExtResponse` and `MessageMCPResponse` are both
`jsontext.Value`) become one type-set term and one rule group; the generator resolves aliases through
the schema, so declaration order does not matter. Each alternative contributes a `union.Rule` —
required and non-nullable members, literal tags, scalar literal, null — kept in a per-union
`union.Table`; `As` decodes once any rule for `T` accepts the payload and otherwise reports why
not, and `New<Union>` rejects values that match no rule. Alternatives are named after their
literal members, `Custom` for a catch-all with an index signature, or else the required members
no sibling has; alternatives that would share a name get the members that set them apart within
that group appended (`CreateElicitationRequestFormSession`, `…FormRequest`). An id member names
what it identifies (`sessionId` gives `Session`), since `…SessionID` would read as an identifier
type. Object types with required literal members (`CreateElicitationRequestFormSession.Mode`) fix
those members in their own `MarshalJSONTo`, so a zero value encodes as its alternative with or
without the constructor.
Generated types implement only the JSON v2 method pair; `encoding/json` honors it as well.

Tagged unions offer `As[T]` over their `<Type>Variant` types
alongside the `Variant()` type switch; there it returns `(T, bool)` like a type assertion, since
the variant is already decoded:

```go
if chunk, ok := n.Update.As[schemav1.SessionUpdateAgentMessageChunk](); ok { ... }
```

Streaming `MarshalJSONTo` / `UnmarshalJSONFrom` methods
integrate with JSON v2 encoders and decoders. Stored JSON is copied on decode and when returned
to the caller, so decoding a copied union value does not mutate the original.

The generator first processes both versions before writing output, and `-check` detects stale
checked-in output without modifying it. Generator tests cover parsing failures, numeric hints,
compilation and JSON round trips of generated Go code, table tests for the pure helpers (alias
resolution, alternative rules, import detection), plus both pinned SDK versions.

## Generated API naming

Method constants use Go-style names such as `AgentMethodsSessionNew` and
`ClientMethodsSessionUpdate`, grouped in one `const` block per SDK table. `CurrentProtocolVersion`
is the numeric version constant; `ProtocolVersion` is the wire type. Names keep Go's
initialisms in one case, such as `RequestID`, `MCPServerHTTP`, `LLMProtocol` and
`PositionEncodingKindUTF16`; the list lives in `initialisms` in `internal/cmd/schema/tsgen`, and
`spellings` fixes words like `OpenAI`; `literalNames` names constants whose value says nothing
about their meaning (`ErrorCodeParseError` for `-32700`).
Tagged-union variants are named `<Union><TagValue>`; when an unrelated schema type already has
that name, the variant gets a `Variant` suffix (a generator test fails if that happens in the
pinned schemas).
Open enums report protocol-defined values with `Known`. Collisions involving enum constants and
constructors fail generation rather than producing Go code that cannot compile.

Doc comments come from the SDK, turned into Go doc comments by rules in
`internal/cmd/schema/tsgen/doc.go` that apply to every declaration:

- An opening that takes it is led by the declared name (`SessionID is a unique identifier…`,
  `X is a request to…`); an opening that is already a sentence is kept, and the generated
  paragraph naming the declaration goes first instead.
- `**UNSTABLE**` paragraphs and `@experimental` tags become one closing
  `Experimental: not part of the spec yet; it may change or be removed.` paragraph, the wording
  the façade tables use too.
- Markdown links become Go doc links with their definitions at the end, Rust intra-doc links
  (``[`ContentBlock::Text`]``) become links to the Go declaration (`[ContentBlockText]`), and
  list items are indented so godoc renders them as lists.
- The extensibility sentences the SDK repeats on every `_meta` member are dropped; `Meta`
  documents them once.

## Zod-aware decoding

`Validated()` returns the `json.Options` that apply the SDK's Zod rules to every generated
type met while unmarshaling, at any nesting depth. The generic `Decode` and `Validate`
functions do the same for one top-level value:

```go
var req acp2.PromptRequest
err := json.Unmarshal(data, &req, acp2.Validated())
req, err = acp2.Decode[acp2.PromptRequest](data)
err = acp2.Validate[acp2.RequestPermissionRequest](data)
```

`Decode` validates and normalizes according to the supported Zod rules, then decodes
into the generated Go type. `Validate` reports whether that same Zod parser accepts the
input, **including recovery/default behavior**; it is not a strict no-recovery validator.
The one intended difference from the SDK is `OpenTags`: unknown tags of tagged unions without a
catch-all or default variant are accepted (see above).
Rules are emitted as typed Go composite literals (`zod.Rule`) so mistakes fail at compile time.
A subtree that repeats across schemas, such as the `_meta` rule on every object, is emitted
once as a package variable named from its contents (`zodRefSessionId` for a reference, the kinds
and a content hash otherwise), so unrelated schema changes do not rename it;
regular expressions are compiled once at package init, when `zod.Link` also prepares the rules
for evaluation without changing what they accept: it resolves references, reads bounds and
literals once, merges an intersection of objects with distinct properties into one object, and
indexes a tagged union by its tag, so a value is checked against the variant its tag names (or
the custom catch-all) instead of against each variant in turn. Evaluation parses the input once into a tree
that shares the input's bytes, applies the rules to the tree, and encodes the result once;
a part no rule changed is copied as the input wrote it, so the normalized JSON keeps the
input's property order and spacing there. Plain `json.Unmarshal` without
`Validated` and raw-union `As[T]` stay lenient.

Identifier and other scalar SDK types are distinct Go types (`type SessionID string`), so they
carry their own rule and cannot be mixed up. Types that are Go aliases (`ExtRequest` and the other `jsontext.Value`
payloads, plus nullable scalars) share a `reflect.Type` with their
underlying type: `Validated` decodes them as that type and `Decode`/`Validate` reject them.

Missing input and JSON null remain distinct while applying rules. Defaults apply to missing
values; recovery may omit an invalid optional field or replace it with a literal fallback.
`requiredDefaultOnError` rejects a missing key while recovering a present invalid value.
`vecSkipError` removes invalid array items. Objects strip unknown keys, while custom union
variants restore unevaluated payload keys, preserving large JSON numbers. Reserved known tags
cannot use a custom catch-all to bypass a malformed known variant. Errors include schema and
field/index paths. Integer-valued JSON such as `1.0` is normalized before Go integer decoding.

This is the SDK's static Zod subset, not a JavaScript runtime or general Zod interpreter.
Arbitrary transforms/refinements, regex flags and unsupported options fail generation. Regexes
use Go's regexp engine; only expressions in the supported subset are accepted. URLs use Go's
absolute-URL parser, which is stricter than WHATWG for inputs such as `http:example.com`;
it is not a complete WHATWG implementation. ISO datetimes require seconds, validate dates and
respect the `offset` option. Input JSON rejects duplicate keys and invalid UTF-8. Schema
recursion is limited to 512 evaluator levels. Error wording is Go-specific.

Both pinned SDK versions are covered by Go tests and captured reference outcomes from
Zod 4.6.5; see [reference fixtures](testdata/README.md). A snapshot of the evaluator's results on a
corpus derived from every rule (`testdata/zod-snapshot-v{1,2}.json.gz`) guards changes to the
evaluator or the generated rules; rewrite it with
`go test ./schema/... -run TestZodSnapshot -update-zod-snapshot` only when a result is meant to
change. The pinned SDK helper source is
included at `typescript/schema-deserialize.ts` to document the recovery and extension rules.
