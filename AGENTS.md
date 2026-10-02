ACP(Agent Client Protocol) for golang

## reference for implement

### Docs

- What is ACP `reference/agent-client-protocol/docs/get-started/introduction.mdx`
- Protocol Details `reference/agent-client-protocol/docs/protocol`

### Go baseline

- Both modules require Go 1.27 or newer.
- New schema code uses `encoding/json/v2` and `encoding/json/jsontext`.
- Use generics for reusable typed operations; do not maintain pre-generics compatibility.
- Optional pointer fields use `omitzero` so explicit empty values survive JSON v2 encoding.

### Schema generation

- Inputs: `schema/typescript/v1/*.ts`, `schema/typescript/v2/*.ts`
- Upstream revision: `schema/typescript/REVISION`
- Generator: `internal/cmd/schema` (separate Go module)
- Generator overrides (integer types for bare TypeScript numbers): `internal/cmd/schema/overrides.yaml`
- Details: `schema/README.md`
- Outputs: `schema/{v1,v2}/{methods,enums,types,unions,envelope,getters}.gen.go` (wire types by kind, plus nil-safe getters), `schema/{v1,v2}/zod.gen.go` (Zod rule tables),
  plus `types.gen.go`/`methods.gen.go` in the `acp1` and `acp2` façade packages (from `internal/cmd/schema/facade/{v1,v2}.go`)
- Adding or regrouping a protocol method: edit the façade table, run `go generate ./...`; never edit `*.gen.go`
- Each optional façade group names the capability member that advertises it (`Capability`, or `NoCapability`); `CapabilitiesOf`/`ClientCapabilitiesOf` are generated from it
- Members where absent and null differ (Rust `MaybeUndefined`) are listed under `tristate` in `overrides.yaml` and become `optional.Value[T]`
- Shared Zod evaluator: `schema/internal/zod`

### Packages

- Root `acp`: version-neutral runtime API — options, the `Transport` interface and stdio transport, middleware, errors, session store, `TurnTracker`, typed extensions (`CallExt`, `ExtRouter`, and the `ExtMethodHandler`/`ExtNotificationHandler` interfaces the façades alias), `AfterReply`. Imports no façade.
- `acphttp`: Streamable HTTP and WebSocket transports (draft RFD), apart from the root so stdio programs do not link them.
- `acp1` / `acp2`: protocol façades on `schema/v1` / `schema/v2`; symmetric, both import root.
- `acp1/acp1test` / `acp2/acp2test`: test helpers for agents — a recording client and an in-memory `Connect`.
- `router`: `ProtocolRouter` serving both versions on one endpoint, and `ClientConnector` for the client side with v2→v1 fallback (imports root and both façades).
- `internal/jsonrpc`: JSON-RPC 2.0 core. `internal/acpconn`: generic dispatch, process spawn/pipe, client turn buffering and agent-side prompt cancel tracking used by the façades.
- `acpmcp` (separate module, `replace`s the root): MCP-over-ACP bridged to the MCP Go SDK; unstable, like the RFD it implements. Test it from its own directory.
- `schema/meta`: the `_meta` map type every schema version aliases as `Meta`. `schema/optional`: `optional.Value[T]`, the absent/null/value type of the members `overrides.yaml` lists as tristate. `schema/internal/union`, `schema/internal/zod`: generated-code runtimes, importable only by the schema packages.

### SDK

- Typescript SDK `reference/typescript-sdk`
- Python SDK `reference/python-sdk`
- Rust Lib `reference/agent-client-protocol`
