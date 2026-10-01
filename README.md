![Agent Client Protocol Go banner](./docs/imgs/banner-dark.jpg)

# Agent Client Protocol for Go

English | [한국어](./docs/README.ko.md)

Build coding agents and editor clients that communicate through the Agent Client Protocol (ACP).
This Go SDK provides typed APIs, streaming session updates, cancellation, and stdio, HTTP, and WebSocket connections.

This is an **unofficial** implementation. ACP v1 is stable; ACP v2 and the HTTP transport specification are drafts.
The protocol is evolving, so support follows the [pinned upstream revision](schema/typescript/REVISION).
See the [official ACP documentation](https://agentclientprotocol.com/) for the protocol itself.

| Protocol | Schema version |
| --- | --- |
| ACP v1 | [`1.24.1`](https://github.com/agentclientprotocol/agent-client-protocol/blob/main/schema/v1/CHANGELOG.md) |
| ACP v2 (draft) | [`2.0.0-alpha.7`](https://github.com/agentclientprotocol/agent-client-protocol/blob/main/schema/v2/CHANGELOG.md) |

[Quick start](#quick-start) · [Packages](#packages) · [Examples](#examples) · [Documentation](#documentation)

## Requirements and Installation

Requires **Go 1.27+** and uses `encoding/json/v2`.
Run this in your Go module to add the SDK:

```bash
go get github.com/ironpark/acp-go
```

## Quick Start

Run an agent and client in the same process, with no API key or external agent required:

```bash
git clone https://github.com/ironpark/acp-go.git
cd acp-go
go run ./examples/inprocess
```

Expected output:

```text
>> hello
<< HELLO
>> same process, no child
<< SAME PROCESS, NO CHILD
```

The [complete example](examples/inprocess/main.go) connects both sides with `acp1.Pipe`.
Once connected, the client initializes the agent, creates a session, and sends a prompt.
This excerpt shows that flow; `agent` and `ctx` are created earlier in the example:

```go
if _, err := agent.Initialize(ctx, &acp1.InitializeRequest{}); err != nil {
    log.Fatal(err)
}
session, err := agent.StartSession(ctx, &acp1.NewSessionRequest{Cwd: "/"})
if err != nil {
    log.Fatal(err)
}
turn, err := session.Prompt(ctx, acp1.TextBlock("hello"))
if err != nil {
    log.Fatal(err)
}
text, _, err := turn.Text()
if err != nil {
    log.Fatal(err)
}
fmt.Println(text) // HELLO
```

To build your own **agent**, start with [Echo Agent](examples/echo/): implement `Initialize`,
`NewSession`, `Prompt`, and `CancelSession`. Optional interfaces add capabilities such as loading sessions.

To build a **client**, start with [Client](examples/client/): use `acp1.SpawnAgent` to launch a stdio
agent and implement `SessionUpdate` and `RequestPermission` to handle its updates and permission requests.
See the [SDK guide](docs/guide.md#implementing-an-agent-and-client) for connection setup and capability detection.

## Packages

Start with `acp1` for ACP v1. Add other packages as your application needs them.

| Package | When to use it |
| --- | --- |
| [`acp1`](acp1/) | Build an agent or client for stable ACP v1. |
| [`acp2`](acp2/) | Work with draft ACP v2; APIs may change. |
| [`acp`](./) | Configure shared transports, middleware, errors, stores, and typed extensions. |
| [`acphttp`](acphttp/) | Connect remote agents over Streamable HTTP or WebSocket (draft). |
| [`router`](router/) | Serve both protocol versions, or connect with v2 → v1 fallback. |
| [`acpmcp`](acpmcp/) | Bridge MCP over ACP (separate module, unstable). |
| [`schema/v1`](schema/v1/) / [`schema/v2`](schema/v2/) | Access generated wire types and validation rules. |

## Examples

Choose an example by what you want to build. The [examples guide](examples/README.md) has run commands and setup details.

| Goal | Example |
| --- | --- |
| Try an agent and client in one process | [In-process](examples/inprocess/) |
| Implement the smallest agent | [Echo Agent](examples/echo/) |
| Add sessions, modes, tools, and permission requests | [Agent](examples/agent/) |
| Interact with a stdio agent | [Client](examples/client/) |
| Build a model-backed coding agent using OpenRouter | [Open Agent](examples/open-agent/) |
| Connect over HTTP or WebSocket and reload a session | [HTTP Agent](examples/http-agent/) / [HTTP Client](examples/http-client/) |
| Support both v1 and v2, including fallback | [Dual Agent](examples/dual-agent/) / [Dual Client](examples/dual-client/) |
| Expose client-provided MCP servers to an agent | [MCP over ACP](acpmcp/) |

## Documentation

- [SDK guide](docs/guide.md) — architecture, agent/client implementation, and API usage.
- [Sessions and cancellation](docs/guide.md#sessions) — manage session state and prompt turns.
- [Streaming updates](docs/guide.md#sessionstream) — send text, tool calls, and plans.
- [Transports](docs/guide.md#transport-layer) — stdio, HTTP, WebSocket, and reconnection.
- [Version routing](docs/guide.md#serving-v1-and-v2-together) — serve both versions and configure fallback.
- [Protocol support](docs/protocol-support.md) — method/interface tables and v1/v2 behavior differences.
- [Schema generation](schema/README.md) — upstream inputs, regeneration, and current limits.

## Contributing

Issues and pull requests for the Go SDK are welcome. For protocol changes, use the
[official ACP repository](https://github.com/agentclientprotocol/agent-client-protocol).

Build and test the main module from the repository root:

```bash
go build ./...
go test ./...
```

The MCP bridge and schema generator are separate modules; run their tests from `acpmcp/`
and `internal/cmd/schema/`, respectively. See the [MCP guide](acpmcp/README.md) and
[schema guide](schema/README.md) for details.

## License

See [LICENSE](LICENSE) for the license terms.
