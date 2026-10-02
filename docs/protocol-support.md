# Protocol Support

[README](../README.md) | [SDK guide](guide.md) | [한국어](protocol-support.ko.md)

This reference covers ACP v1 and draft v2, based on the
[pinned upstream revision](../schema/typescript/REVISION).
Interfaces belong to the corresponding `acp1` or `acp2` package.
`required` identifies methods on the base interface; `unstable` marks features that may change.

[Version comparison](#version-comparison) · [v1 agent](#agent-methods-client--agent) · [v1 client](#client-methods-agent--client) · [v2](#acp-v2-acp2-draft)

## Version Comparison

| Behavior | ACP v1 | ACP v2 (draft) |
| --- | --- | --- |
| Package | `acp1` | `acp2` |
| File and shell access | `fs/*`, `terminal/*` | Through MCP |
| Prompt response | Returns the turn result | Acknowledges message acceptance |
| Turn completion | Prompt response | Agent reports idle state |
| Prompt during a running turn | Rejected with `acp.ErrTurnInProgress` | Joins the running turn |

The next two tables describe **ACP v1**.

## Agent methods (client → agent)

| Method | Go interface |
| --- | --- |
| `initialize`, `session/new`, `session/prompt`, `session/cancel` | `Agent` (required) |
| `authenticate` | `Authenticator` |
| `session/load` | `SessionLoader` |
| `session/list` | `SessionLister` |
| `session/delete` | `SessionDeleter` |
| `session/fork` | `SessionForker` (unstable) |
| `session/resume` | `SessionResumer` |
| `session/close` | `SessionCloser` |
| `session/set_mode` | `SessionModeSetter` |
| `session/set_config_option` | `SessionConfigOptionSetter` |
| `providers/list`, `providers/set`, `providers/disable` | `ProviderManager` (unstable) |
| `logout` | `LogoutHandler` |
| `nes/*` | `NesHandler` (unstable) |
| `document/did*` | `DocumentHandler` (unstable) |
| `mcp/message` (notifications) | `MCPMessageHandler` (unstable) |

## Client methods (agent → client)

| Method | Go interface |
| --- | --- |
| `session/update`, `session/request_permission` | `Client` (required) |
| `fs/read_text_file` | `FileReader` |
| `fs/write_text_file` | `FileWriter` |
| `terminal/*` | `TerminalHandler` |
| `elicitation/create`, `elicitation/complete` | `ElicitationHandler` |
| `mcp/message` (requests) | `MCPProvider` (unstable) |

`$/cancel_request` is handled by the connection itself.

## ACP v2 (`acp2`, draft)

### Agent methods (client → agent)

| Method | Go interface |
| --- | --- |
| `initialize`, `session/new`, `session/list`, `session/resume`, `session/close`, `session/prompt`, `session/cancel` | `Agent` (required) |
| `auth/login`, `auth/logout` | `AuthHandler` |
| `session/delete` | `SessionDeleter` |
| `session/fork` | `SessionForker` |
| `session/set_config_option` | `SessionConfigOptionSetter` |
| `providers/*` | `ProviderManager` (unstable) |
| `nes/*` | `NesHandler` (unstable) |
| `document/did*` | `DocumentHandler` (unstable) |
| `mcp/message` (notifications) | `MCPMessageHandler` (unstable) |

### Client methods (agent → client)

| Method | Go interface |
| --- | --- |
| `session/update`, `session/request_permission` | `Client` (required) |
| `mcp/message` (requests) | `MCPProvider` (unstable) |
| `elicitation/create`, `elicitation/complete` | `ElicitationHandler` |

### Turn behavior

v2 has no `fs/*` or `terminal/*` methods — file and shell access go through MCP — so
`TerminalHandle` exists only in the v1 package. In v2 the prompt response only accepts the message;
a `Turn` ends when the agent reports the idle state.

Overlapping prompts follow each version's rules on both sides. A v1 session runs one turn at a
time: `ClientSession.Prompt` and `SessionManager.RunTurn` (or `BeginTurn`) both refuse a second
prompt with `acp.ErrTurnInProgress` (`-32600`). In v2 a prompt may contribute to running work:
`Prompt` returns `(*Turn, MessageID, error)` once the message is accepted and joins the running
turn, and `SessionManager.StartTurn` (or `JoinTurn`) hands the agent the running turn's context,
reporting running and idle around its work.

### Cancellation ordering

A `session/cancel` sent after a prompt always reaches that prompt's turn. `ClientSession.Prompt`
returns only once the prompt is queued, so a `Cancel` after it follows it on the wire, and a cancel
that arrives before the agent's handler starts its turn still starts that turn
cancelled with `acp.ErrTurnCancelled`.
