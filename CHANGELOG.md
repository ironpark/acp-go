# Changelog

All notable changes to this module are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Unstable areas
(`acpmcp`, MCP-over-ACP, subagents, providers) may change in any release.

## [Unreleased]

## [0.5.0] - 2026-11-13

### Schema

- Pinned the TypeScript SDK at `8649e0a` (2.6.0). The ACP schema moves to **v1.31.0**, and the v2
  draft to **2.0.0-alpha.15**. Every change is an addition.
- **`session/options` (unstable)** → `SessionOptionsReader` (`GetSessionOptions`), advertised as
  `sessionCapabilities.options` (v1) / `session.options` (v2). It reports the config options (and
  v1 modes, v2 commands) a session would start with, without creating or resuming one.
- **Initial config option values (unstable):** `session/new`, `session/resume` and v1
  `session/load` take `configOptions` (`[]InitialSessionConfigOption`), which an agent
  advertising the options capability applies when it sets the session up. Unknown or mismatched
  entries are ignored rather than rejected.
- **Session tool call content (unstable):** a tool call may show another session it works on, such
  as a subagent (`ToolCallContentSession`); `ToolSession(id)` builds it in both façades.
- `AuthAccount` gains an optional `Description`.
- The client system prompt (`systemPrompt` on session setup requests) is stable as of schema
  v1.30.0; the Go API is unchanged.
- Reference fixtures gain cases for the new types, checked against Zod 4.6.7.

### Fixed

- Streamable HTTP: a client may open a session's SSE stream, and send `session/load` or
  `session/resume` with `Acp-Session-Id`, before the session exists on the connection. This is
  the reconnect flow of the transport RFD, which the TypeScript SDK follows; its clients got
  `404 unknown session` and dropped the connection. Other session-scoped methods, such as
  `session/prompt`, still need the session established by a successful load or resume. A stream
  opened for a session that is never attached goes with its GET, and a connection holds at most
  64 of them (`409` beyond that).
- Streamable HTTP: `session/resume` attaches the session to the connection, on the server and in
  `ClientTransport`. A session resumed over HTTP had no session stream, so the prompts that
  followed were answered `404 unknown session`.

## [0.4.0] - 2026-11-09

### Schema

- Pinned the TypeScript SDK at `8d95327` (2.4.0). The ACP schema moves to **v1.29.1**, and the v2
  draft to **2.0.0-alpha.13**. The SDK now ships stable and unstable views; the snapshot takes the
  unstable views (`src/{v1,v2}/unstable/schema`), which hold every method, as before.
- **New methods (unstable), in both versions:**
  - `session/archive` / `session/unarchive` → `SessionArchiver`, advertised as
    `sessionCapabilities.archive` (v1) / `session.archive` (v2), with the `archived` filter on
    `session/list` (`SessionArchiveFilter`) and `SessionInfo.Archived`.
  - `session/status` → `SessionStatusReader` (`GetSessionStatus`), reporting `open` or `saved`.
  - `session/set_title` → `SessionTitleSetter`, advertised as `…title`.
  - `auth/status` → `AuthStatusReader` (`GetAuthStatus`), advertised as `auth.status`; the agent's
    `auth/status_update` notification reaches clients implementing `AuthStatusListener`.
  - `CapabilitiesOf` sets each capability from the interface the agent implements.
- **v2 cancellation of single tool calls and terminals:** `tool_call/cancel` → `ToolCallCanceller`
  and `terminal/cancel` → `TerminalCanceller`. Each tool call or terminal advertises
  `capabilities.cancel` itself, so these groups have no initialize capability.
  `acp2.WithCancellable(bool)` sets or clears it on the tool call updates `SessionStream` sends.
- v2 also gains background work reporting (`BackgroundWork`), the `paused` tool call status,
  terminal sizes, typed error data (`ErrorData`, `SessionNotFoundErrorData`,
  `UnsupportedProtocolVersionErrorData`), and error responses with a `null` id.
- The proxy-chain methods `proxy/initialize` and `proxy/successor` are generated as types and
  `ProxyMethods*` constants only; like the SDK, the façades do not route them.
- **64-bit integers:** the SDK marks every `i64`/`u64` field with `wideInt()`, so the generator
  takes their Go types from the Zod rules and `overrides.yaml` no longer lists any number.
  Validation accepts integers beyond 2^53 for them, as Rust peers send. v2 `TerminalExitStatus.ExitCode`
  becomes `*int64` and v2 `ResourceLink.size` `*uint64`, matching the Rust schema.
  `ElicitationContentValue` gains an `int64` alternative ahead of `float64`; `As[float64]` still
  reads integers.
- **Null lists:** `mcpServers`, `additionalDirectories`, terminal `args`/`env`, MCP `headers` and
  similar lists read an explicit `null` as empty, as Rust's `DefaultOnNull` does, while a missing
  required list is still rejected.
- v2 elicitation scopes require exactly one of `sessionId` or `requestId`; a request carrying both,
  or neither, fails validation.
- More members distinguish absent from `null` (`optional.Value`): v1/v2 `session_message`
  participant ids, subagent `cwd`/`additionalDirectories`, and in v2 `usage_update.cost`, tool call
  and terminal `capabilities`, and terminal `size`.

### Added

- `acp2.SlashCommand(name, description)` builds the common available command, and
  `acp2.CommandOf` reads one back.
- **Per-tool-call cancellation for v2 agents:** `acp2.SessionManager.TrackToolCall` and
  `TrackTerminal` return a context that `tool_call/cancel` / `terminal/cancel` cancel with
  `acp.ErrWorkCancelled` (`acp.WorkCancelled` checks it). The manager now serves both requests,
  refusing untracked ids with `-32602` as the protocol asks. The version-neutral
  `acp.WorkTracker[K]` is the building block.
- **Session-bound client calls:** `ClientSession.SetTitle`, `Archive`, `Unarchive` and `Status` in
  both versions, `CancelToolCall` and `CancelTerminal` in v2, and `Close` in v1. v2 `Archive`
  releases the session's update queue, as `Close` does, since archiving closes the session.
- `ClientSideConnection.AuthStatus()` (both versions) returns the agent's last reported auth
  status, from `GetAuthStatus` or an `auth/status_update`, whether or not the client implements
  `AuthStatusListener`.
- Error data the protocol defines: `acp.SessionNotFound(id)` / `acp.SessionNotFoundID(err)` for
  `-32002` with `data.sessionId`, and `acp.UnsupportedProtocolVersion(requested, supported...)` /
  `acp.SupportedProtocolVersions(err)` for the new `-32022` (`acp.ErrorCodeUnsupportedProtocolVersion`).
  `RequestError.DecodeData` reads any error's data, local or received.
- `acp.SessionListQuery.Archived` passes session/list's archived filter to a `SessionInfoLister`.

### Changed

- `SessionManager.List` (v1) / `ListSessions` (v2) apply session/list's `archived` filter: by default
  they leave out sessions whose `SessionInfo.Archived` is true, `include` and `only` select the
  rest, and an unknown value is refused with `-32602` instead of being read as `exclude`.
- `SessionManager.Lookup` answers an unknown session with `acp.SessionNotFound`, so the error now
  carries `data.sessionId`; the code and message are unchanged.
- The router answers an initialize below every version it serves with `-32022` and
  `{requested, supported}` data instead of `-32600`. An `acp2` agent refuses an initialize below
  v2 the same way before its `Initialize` runs.
- `router.ClientConnector` reads a `-32022` answer: it retries with v1 when the agent lists it, and
  otherwise fails with `ErrNoCommonVersion`.

### Breaking changes

- **v2 `AvailableCommand` is a tagged union** (`type: "command"`); build one with
  `acp2.SlashCommand` or `NewAvailableCommand(AvailableCommandCommand{…})`.
- **v2 `AuthHandler` no longer sets the `auth` capability**: a non-empty `authMethods` list
  advertises `auth/login` and `auth/logout`. v2 `AuthCapabilities` is renamed
  `ClientAuthCapabilities`.
- v2 `Error.Data` is `ErrorData` (read it with `As` or `RawJSON`); `AgentResponseError.ID` and
  `ClientResponseError.ID` are pointers, nil for a `null` id. `ProtocolLevelNotification` is removed.
- v2 `ElicitationSchema.Type` and `.Properties` are required (non-pointer).
- `session_message_chunk` no longer carries `senderSessionId`/`recipientSessionId`; they moved to
  `session_message`, as `optional.Value[SessionID]`.
- v2 `SessionUpdateUsageUpdate.Cost` is `optional.Value[Cost]`. `SessionStream.SendUsage` keeps its
  signature; a nil cost now leaves the client's cost unchanged instead of omitting it.
- v2 `Cost.Currency` accepts any unit (such as credits), not only three-letter ISO 4217 codes,
  as v1 already did.

### Generator

- The Zod reader and evaluator support the SDK's new helpers `wideInt`, `defaultOnNull` and
  `exactlyOneByRequiredKeys`. Reference fixtures were refreshed against Zod 4.6.7 and extended
  with cases for each helper.
- `schema/update.sh` follows the SDK's new source layout.
- A method in an optional façade group may use `Via`/`CallVia`; the dispatch then reaches the
  hand-written method whether or not the peer implements the interface.

## [0.3.0] - 2026-10-19

### Schema

- Pinned the TypeScript SDK at `1db4d43` (1.9.0). The ACP schema moves to **v1.26.0**, and the v2
  draft to **2.0.0-alpha.9**.
- **Client system prompt (unstable):** `session/new`, `session/load` (v1), `session/fork` and
  `session/resume` take an optional `systemPrompt` (`SystemPrompt`, with an open `SystemPromptMode`
  of `append` or `override`). Agents advertise it as `SessionCapabilities.SystemPrompt`.
- **Next Edit Suggestions are removed** from the protocol, with the `nes/*` and `document/did*`
  methods and the client's `positionEncodings` / agent's `positionEncoding`.
- v2 documents a concrete `_meta` in an update as a patch of the stored metadata by top-level key.
  The members already decode as `optional.Value[Meta]`, so absent, `null` and an object stay
  distinct.

### Breaking changes

- **NES is gone:** `NesHandler`, `DocumentHandler`, their `nes/*` and `document/did*` calls on
  `ClientSideConnection`, and the `Nes*`, `*DocumentNotification`, `Position`, `Range`,
  `PositionEncodingKind`, `TextDocument*` and `WorkspaceFolder` types are removed from `acp1`,
  `acp2` and both schema packages. `AgentCapabilities.Nes` / `PositionEncoding` and
  `ClientCapabilities.Nes` / `PositionEncodings` are removed as well.
- **The v2 client reads session updates apart from prompts** (TypeScript SDK #280). An idle update
  reports that the session's foreground work stopped, not that a prompt was processed, so a turn
  tied to a prompt could end on the idle of earlier work and drop the updates that followed.
  - `acp2.ClientSession.Prompt` returns `(MessageID, error)`: the insertion receipt. A rejection is
    reported there only and no longer ends a turn.
  - Each `ClientSession` queues its session's updates from the moment `Session` or `StartSession`
    creates it. `ClientSession.UntilIdle(ctx)` returns a `Turn` that reads them up to and including
    the next idle update; `Turn.Updates`, `Wait` and `Text` keep their meaning, and `Turn.Done` is
    removed. `ClientSession.NextUpdate(ctx)` reads one update.
  - `ClientSession.Close(ctx)` sends `session/close` and releases the queue.

### Fixed

- Requests with `"params": null` decode as an empty object, as omitted params already did, so
  all-optional requests such as `session/list` and `logout` are accepted. Requests with required
  fields still fail with `-32602`.

## [0.2.0] - 2026-10-12

### Schema

- Pinned the TypeScript SDK at `f4ea219` (1.8.0). The ACP schema moves to **v1.25.0**, and the v2
  draft to **2.0.0-alpha.8**.
- **Error stop reason (unstable):** work that fails after a v2 prompt was accepted ends idle with
  `stopReason: "error"` and a JSON-RPC error object, since the prompt response can no longer
  report it. The idle state's stop reason is now a union tagged by `stopReason`, flattened into the
  idle update. v1 subagent states get the same stop reason; the v1 prompt response is unchanged.
- **Stricter fields:** malformed values that a receiver acts on are rejected instead of being
  replaced by a fallback. Examples are `terminal/create` arguments, `fs/read_text_file` `line` and
  `limit`, `mcpServers`, `additionalDirectories`, `replayFrom` and `didChange` edits.
- Session notices and session compaction are stable.

### Breaking changes

- **Idle states are tagged unions:**
  - `StateUpdateIdle` in both schema versions wraps an `IdleStateUpdate`, with one variant per
    stop reason: `IdleStateUpdateEndTurn`, …, `IdleStateUpdateError`, `IdleStateUpdateCustom`, and
    `IdleStateUpdateUntagged` for none. It no longer has a `StopReason` field.
  - `schema/v2` no longer has a `StopReason` type. `acp2.StopReason` is now declared by `acp2`,
    with the same constants plus `StopReasonError`.
- **`acp2.SessionManager.StartTurn` work returns `(StopReason, error)`.** An error, or a panic,
  ends the turn with `StopReasonError`.
- **`acp2.StopReasonInternalError` (`_internal_error`) is removed**, replaced by the standard
  error stop reason.
- **The client's `Turn.Wait` and `Turn.Text` report the error stop reason** as
  `acp2.StopReasonError` together with the agent's error as an `*acp.RequestError`.

### Added

- `SessionStream.IdleError` and `SubagentStream.IdleError` in `acp2`, and
  `SubagentStream.IdleError` in `acp1`, report failed work with the error stop reason.
- The generator reads TypeScript template literal types, the `UnknownVariant<T>` brand and the
  `defaultOnErrorOptionalStringTag` Zod helper. It also narrows a member that an intersection
  declares twice, and treats a `tag?: null` alternative as a tagged union's untagged default.

### Changed

- `acpmcp` requires the released `github.com/ironpark/acp-go v0.1.0` instead of a `replace` of the
  root, so it can be fetched on its own. Its first tag is `acpmcp/v0.1.0`. A `go.work` at the
  repository root keeps local development on the working tree.

## [0.1.0] - 2026-10-02

First tagged release. It supports ACP schema v1.24.1 and the v2 draft 2.0.0-alpha.7. The entries
below cover the changes made since `c7b0f78`.

### Schema

- Pinned the TypeScript SDK at `a1a80ff` (1.6.0). The ACP schema moves from v1.23.0 to
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

[Unreleased]: https://github.com/ironpark/acp-go/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/ironpark/acp-go/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/ironpark/acp-go/compare/v0.1.0...v0.2.0
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
