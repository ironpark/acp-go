# acpmcp — MCP over ACP

> [!WARNING]
> **Unstable.** MCP-over-ACP is an RFD-stage draft of the Agent Client Protocol
> ([RFD](https://agentclientprotocol.com/rfds/mcp-over-acp)), not part of the
> stable protocol. The TypeScript and Python SDKs mark it unstable and the Rust SDK
> gates it behind the `unstable_mcp_over_acp` feature. The wire format, and with it
> this module's API, may change in any release.

`acpmcp` lets an ACP client hand its agent MCP servers that live in the client's own
process. Tool calls travel over the ACP connection the two already share, through the
`mcp/message` method, with no stdio child or HTTP port in between. Both ends are the
official [MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk): the client serves an
`*mcp.Server`, the agent talks to it through an `*mcp.ClientSession`.

The binding carries stateless MCP **2026-07-28** only. Each MCP request is an `mcp/message`
request of its own, addressed by `serverId` and identified by a fresh `requestId`; there is
no MCP connection, `initialize` handshake or connect/disconnect exchange. An MCP error comes
back as the response's `error` outcome, apart from the binding's own ACP errors
(`ErrorCodeServerUnavailable`, …).

It is a separate module so that the SDK does not depend on the MCP SDK.

```sh
go get github.com/ironpark/acp-go/acpmcp
```

## Client: provide a server

Embed a `HostV1` (or `HostV2`) in the client; it answers the agent's `mcp/message` requests,
serving each on an MCP session of its own, as a stateless MCP server serves each HTTP request.
`Add` registers a server and returns its entry for `session/new`:

```go
type myClient struct {
    *acpmcp.HostV1
    // SessionUpdate, RequestPermission, ...
}

agent, _ := acp1.SpawnAgent(ctx, cmd, func(conn *acp1.ClientSideConnection) acp1.Client {
    client = &myClient{HostV1: acpmcp.NewHostV1(conn)}
    return client
})
init, _ := agent.Initialize(ctx, &acp1.InitializeRequest{})
// Offer it only if init.AgentCapabilities.MCPCapabilities.ACP is true.
session, _ := agent.StartSession(ctx, &acp1.NewSessionRequest{
    Cwd:        cwd,
    MCPServers: []acp1.MCPServer{client.Add("project-tools", server)}, // server is an *mcp.Server
})
```

## Agent: use the server

Embed a `DialerV1` (or `DialerV2`) in the agent. It carries the servers' notifications back
to the agent, and `acp1.CapabilitiesOf` then advertises `mcpCapabilities.acp`
(`acp2.CapabilitiesOf`: `session.mcp.acp`). `Connect` opens an MCP session to an `"acp"` entry of the
session's `MCPServers`, as `mcp.Client.Connect` does over any other transport:

```go
func (a *myAgent) NewSession(ctx context.Context, params *acp1.NewSessionRequest) (*acp1.NewSessionResponse, error) {
    for _, server := range params.MCPServers {
        if entry, ok := server.As[acp1.MCPServerACP](); ok {
            tools, err := a.Connect(ctx, entry, mcp.NewClient(impl, nil), nil)
            // tools.ListTools, tools.CallTool, ...
        }
    }
    // ...
}
```

`Connect` discovers the server with `server/discover`. Notifications belong to the request
that is running: progress for a tool call, or `tools/list_changed` for the subscription the
session opens when its client has a list-changed handler. Cancelling a call's context sends
`$/cancel_request`, and the tool behind it sees its context cancelled. MCP 2026-07-28 has no
server-to-client requests; interactive tools return `input_required` results instead. The
module is version-neutral underneath: `HostV2` and `DialerV2` do the same over ACP v2.

## Example

[`example`](./example/main.go) runs an agent and a client in one process: the client
provides a `word_count` tool, and the agent calls it to answer each prompt.

```sh
cd acpmcp && go run ./example
```
