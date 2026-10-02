# Changelog

All notable changes to this module are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Unstable areas
(`acpmcp`, MCP-over-ACP, subagents, NES, providers) may change in any release.

## [Unreleased]

### Changed

- `acpmcp` requires the released `github.com/ironpark/acp-go v0.1.0` instead of a `replace` of the
  root, so it can be fetched on its own. Its first tag is `acpmcp/v0.1.0`. A `go.work` at the
  repository root keeps local development on the working tree.

## [0.1.0] - 2026-10-02

First tagged release. It supports ACP schema v1.24.1 and the v2 draft 2.0.0-alpha.7. The entries
below cover the changes made since `c7b0f78`.

### Schema

- Pinned the TypeScript SDK at `a1a80ff` (1.5.1). The ACP schema moves from v1.23.0 to
  **v1.24.1**, and the v2 draft from 2.0.0-alpha.5 to **2.0.0-alpha.7** ([ce46ac9]).
- MCP-over-ACP is now request-scoped. `mcp/connect` and `mcp/disconnect` are gone. Each
  `mcp/message` request names its server and a `requestId`, and answers with an MCP result or an
  MCP error outcome ([ce46ac9]).
- New in both versions: unstable subagent updates (`subagent_update`, `StateUpdate`). In v2,
  session setup responses also carry the initial `availableCommands` ([ce46ac9]).

### Breaking changes

- **MCP-over-ACP interfaces** ([ce46ac9]):
  - `MCPConnector` is renamed `MCPProvider` and serves `mcp/message` requests only.
  - `MCPMessageHandler` on the agent receives only the notifications of the agent's own requests.
  - Removed: `ConnectMCP`, `DisconnectMCP`, `ConnectMCPRequest`/`Response`,
    `DisconnectMCPRequest`/`Response`, `MCPConnectionID`.
  - Added: `MCPRequestID`, `MCPError`, and the `MessageMCPResponse` result/error alternatives.
- **`acpmcp` targets MCP `2026-07-28` (stateless) only** ([904142a]):
  - The host serves each `mcp/message` request on an MCP session of its own.
  - The dialer turns each request of the agent's MCP session into an `mcp/message` request with
    a fresh `requestId`.
- **`acp2.Agent` requires the v2 session baseline** ([520699c]):
  - `ListSessions`, `ResumeSession` and `CloseSession` are now part of `Agent`, and the optional
    `SessionLister`, `SessionResumer` and `SessionCloser` interfaces are gone. An embedded
    `SessionManager` still provides all three.
  - `acp2.CapabilitiesOf` always advertises the `session` object.
- **Members where null and absence differ are `optional.Value[T]`** ([0849ffa]):
  - These are the members the protocol's Rust schema declares `MaybeUndefined`.
  - Examples: `SessionInfoUpdate.Title`/`UpdatedAt`, the `SubagentUpdate` fields,
    `CompactionUpdate`, `SessionMessage`, and in v2 `ToolCallUpdate`, `TerminalUpdate` and the
    message updates.
  - Write `Title: optional.Of("x")` instead of `Title: new("x")`. Clear a value with
    `optional.Null[T]()`. Getters such as `GetTitle()` still return the plain value.
- **Generated integer types follow the protocol's Rust definitions** ([ce46ac9], [ec16fe7]):

  | Type or member | Before | After |
  | --- | --- | --- |
  | `ErrorCode` | `int64` | `int32` |
  | `MCPError.Code` | `float64` | `int32` |
  | `RequestID` (number alternative) | `float64` | `int64` |
  | `ElicitationPropertySchemaInteger.Minimum`/`Maximum`/`Default` | `*float64` | `*int64` |
  | `ElicitationPropertySchemaArray.MinItems`/`MaxItems` | `*float64` | `*uint64` |

- **Stricter decoding without validation:** a tagged union object without its discriminator,
  where the union has no default variant, now fails to decode. It used to decode as the
  `Unknown` variant. Unknown discriminator values still decode as `Unknown` or the catch-all
  variant ([0849ffa]).
- **v1 `ClientCapabilitiesOf` sets only the flags that are true.** For example, `FileReader`
  alone gives `fs.readTextFile: true` and leaves `writeTextFile` out instead of sending `false`;
  the schema defaults it to false ([0849ffa]).

### Added

- **Subagents** ([cceb749], [00aebad]):
  - `SessionStream.StartSubagent` returns a `SubagentStream` in `acp1` and `acp2`. The stream
    reports the child session's own updates under its id, and its work state through `Running`,
    `RequiresAction`, `Idle` and `Unknown`.
  - In v1 the state goes on the parent's `subagent_update`, which needs the client's `subagents`
    capability. In v2 it goes on the child's stream and is mirrored to the parent.
  - `WithSubagentDescription` and `WithSubagentCancel` set optional fields on the announcement.
  - `acp2.SessionStream.Unknown` reports the new unknown work state.
- **`schema/optional`:** `optional.Value[T]` with `Of`, `Null`, `Get`, `Or`, `IsNull` and
  `IsZero` ([0849ffa]).
- **`acp.WithoutRequestTimeout(ctx)`:** exempts an extension request that waits for the user from
  `WithRequestTimeout` ([e204a53]).
- **`acp2.StopReasonInternalError` (`_internal_error`):** the stop reason of a turn whose work
  panicked ([8004096]).
- **`acpmcp` binding error codes:** `ErrorCodeResourceLimit` (-33000),
  `ErrorCodeServerUnavailable` (-33001) and `ErrorCodeBackendFailed` (-33002) ([904142a]).
- **v2 commands in setup responses:** `session/new`, `session/resume` and `session/fork` responses
  carry the initial `availableCommands`. `SessionManager` fills them from a
  `SessionCommandsReporter` instead of sending an `available_commands_update` after the response
  ([210a843]).
- **README:** a table of the supported schema versions ([af47f15]).

### Changed

- **A v1 prompt's context governs the whole turn** ([32b6e6b]):
  - Cancelling the context passed to `ClientSession.Prompt` now sends `session/cancel`, and the
    turn ends with the agent's cancelled answer, as `exec.CommandContext` stops its process.
  - Before, it sent `$/cancel_request` and abandoned a turn the agent kept running, so the next
    prompt was refused as in progress.
- **Requests that wait for the user are no longer cut off by `WithRequestTimeout`**
  ([32b6e6b], [e204a53]):
  - Affected: v1 `session/prompt`, `session/request_permission` and `elicitation/create`.
  - Their contexts bound them instead.
- **A v2 turn ends when its connection closes** ([0c86c9f]):
  - The turn's context now ends, with the connection's cause, when the connection that started
    it closes.
  - Before, the turn kept running and reported to no one. A reconnecting client resumes the
    session on a new connection.
- **Generated capability helpers** ([0849ffa]):
  - `CapabilitiesOf` and `ClientCapabilitiesOf` are generated from the method table, so every
    optional interface advertises exactly the capability its docs name.
  - Each interface's doc states that capability.
- **Client capabilities are recorded from the validated `initialize` request**, instead of
  decoding the params a second time ([ec16fe7]).

### Fixed

- **Panics in v2 turn work:** a panic in the work `SessionManager.StartTurn` runs took the agent
  down and left the client's turn waiting. The turn now ends idle with `_internal_error` and
  carries the panic under `_meta.error` ([8004096]).
- **`acpmcp` request hangs** ([e32d67c]):
  - A request whose MCP server session broke, for example from a keepalive ping the binding
    cannot carry, waited forever. It now ends with `ErrorCodeBackendFailed`.
  - Pings are answered locally and malformed notifications are dropped.
  - A cancelled request forwards no further notifications.
- **`StartSubagent` on a failed announcement:** it returned a usable-looking stream together with
  the error. It now returns no stream ([00aebad]).
- **v2 `SessionResumer` docs** said resume never replays history. v2 replays it when `ReplayFrom`
  asks ([00aebad]).
- **Zod validation of JSON-RPC errors:** validation accepted a JSON-RPC error response by
  dropping its `error`, because `z.unknown()`/`z.any()` properties did not have to be present. They
  now must be, as in Zod ([ce46ac9]).
- **`z.looseObject` schemas** were unsupported, and their undeclared properties are now kept
  ([ce46ac9]).
- **`McpError.code` was generated as `float64`:** the generator now infers integer types from the
  parsed Zod rules, including `looseObject`, array and record elements, and `int32` ranges
  ([ec16fe7]).
- **The `json:",inline"` tag on variant wire structs** had no effect under `encoding/json/v2`; the
  generator now emits `,embed` ([ec16fe7]).
- **Examples** ([32b5104]):
  - The example client rendered updates out of order with the agent's requests, showing an empty
    permission title and the diff after the prompt. It now renders in `SessionUpdate`.
  - It prints a terminal's output when its tool call ends.
  - The Zed settings snippet in the example READMEs gains its missing brace.

### Internal

- **Generator** ([ec16fe7], [0849ffa]):
  - **Integer hints:** read from the already parsed Zod tree instead of a second, lenient parser.
    An integer rule that matches no number fails generation.
  - **Overrides:**
    - A key may name a definition (`RequestId`).
    - An override the Zod schema already implies is rejected.
    - The new `tristate` lists are kept per version and cross-checked against the Rust schema
      when the reference checkout is present.
  - **Façade table validation:**
    - Each optional group names its `Capability` or sets `NoCapability`.
    - Contradictory `Untimed`/`Via`/`CallVia` combinations are rejected.
  - **Tagged unions:**
    - Variants share one generic wire type and decoder per discriminator name, which removes
      about 1,200 generated lines.
    - Union planning is separated from printing, and `form()` results are memoized.
  - **Tests:** the golden test checks every checked-in generated file through `-check`, which
    includes the getters.
- **Untimed calls** are marked on the request context (`jsonrpc.WithoutTimeout`). The table flags
  them and generates `acpconn.CallUntimed` ([e204a53]).
- **Request contexts carry their connection's context** (`jsonrpc.ConnectionContext`)
  ([0c86c9f], [e204a53]).

[Unreleased]: https://github.com/ironpark/acp-go/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/ironpark/acp-go/releases/tag/v0.1.0
[ce46ac9]: https://github.com/ironpark/acp-go/commit/ce46ac9
[904142a]: https://github.com/ironpark/acp-go/commit/904142a
[210a843]: https://github.com/ironpark/acp-go/commit/210a843
[af47f15]: https://github.com/ironpark/acp-go/commit/af47f15
[cceb749]: https://github.com/ironpark/acp-go/commit/cceb749
[e32d67c]: https://github.com/ironpark/acp-go/commit/e32d67c
[8004096]: https://github.com/ironpark/acp-go/commit/8004096
[00aebad]: https://github.com/ironpark/acp-go/commit/00aebad
[32b5104]: https://github.com/ironpark/acp-go/commit/32b5104
[0c86c9f]: https://github.com/ironpark/acp-go/commit/0c86c9f
[32b6e6b]: https://github.com/ironpark/acp-go/commit/32b6e6b
[520699c]: https://github.com/ironpark/acp-go/commit/520699c
[e204a53]: https://github.com/ironpark/acp-go/commit/e204a53
[ec16fe7]: https://github.com/ironpark/acp-go/commit/ec16fe7
[0849ffa]: https://github.com/ironpark/acp-go/commit/0849ffa
