// Package acpmcp carries MCP over an ACP connection, so a client can hand an
// agent MCP servers that live in the client's own process, with no stdio
// child or HTTP port in between. It connects the official MCP Go SDK
// (github.com/modelcontextprotocol/go-sdk) to the mcp/message method of ACP.
//
// The binding carries stateless MCP 2026-07-28 only. Every MCP request is an
// mcp/message request of its own, naming the server by serverId and itself by
// a fresh requestId; there is no MCP connection or initialize handshake.
// While a request runs, the server's notifications for it, such as progress or
// a subscription's events, come back to the agent under the same ids, and
// cancelling the request's context cancels the ACP request and the tool
// behind it. An MCP error is the response's error outcome, kept apart from
// the outer ACP errors of the binding ([ErrorCodeServerUnavailable] and the
// rest).
//
// The client side is a Host: it registers *mcp.Server values and lists them in
// session/new with the "acp" transport, and serves each request on a session
// of its own, as a stateless MCP server does. The agent side is a Dialer: it
// opens an *mcp.ClientSession to one of those servers.
//
//	// client
//	host := acpmcp.NewHostV1(conn)
//	session, err := conn.NewSession(ctx, &acp1.NewSessionRequest{
//		Cwd:        cwd,
//		MCPServers: []acp1.MCPServer{host.Add("project-tools", server)},
//	})
//
//	// agent, for each MCPServerACP in the session/new request
//	tools, err := dialer.Connect(ctx, acpServer, mcp.NewClient(impl, nil), nil)
//	result, err := tools.CallTool(ctx, &mcp.CallToolParams{Name: "echo"})
//
// Host and Dialer exist for ACP v1 and v2; the wire format is the same.
//
// # Stability
//
// MCP-over-ACP is an RFD-stage draft of the protocol. The TypeScript and
// Python SDKs mark it unstable and the Rust SDK gates it behind the
// unstable_mcp_over_acp feature. Its wire format, and with it this package,
// may still change.
package acpmcp
