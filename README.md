![agent client protocol golang banner](./docs/imgs/banner-dark.jpg)

# Agent Client Protocol - Go Implementation

A Go implementation of the Agent Client Protocol (ACP), which standardizes communication between _code editors_ (interactive programs for viewing and editing source code) and _coding agents_ (programs that use generative AI to autonomously modify code).

This is an **unofficial** implementation of the ACP specification in Go. The official protocol specification and reference implementations can be found at the [official repository](https://github.com/zed-industries/agent-client-protocol).

> [!NOTE]
> The Agent Client Protocol is under active development. This implementation may lag behind the latest specification changes. Please refer to the [official repository](https://github.com/zed-industries/agent-client-protocol) for the most up-to-date protocol specification.

Learn more about the protocol at [agentclientprotocol.com](https://agentclientprotocol.com/).

## Installation

```bash
go get github.com/ironpark/go-acp
```

## Example Code
See the [docs/example](./docs/example/) directory for complete working examples:

- **[Agent Example](./docs/example/agent/)** - Agent implementation with SessionStream, middleware, and permission requests
- **[Client Example](./docs/example/client/)** - Client implementation using SpawnAgent and MatchSessionUpdate

## Architecture

This implementation provides a clean, modern architecture with bidirectional JSON-RPC 2.0 communication:

- **`Connection`**: Unified bidirectional transport layer with concurrent request/response correlation
- **`Transport`**: Pluggable transport interface (stdio, HTTP+SSE) for flexible deployment
- **`AgentSideConnection`**: High-level ACP interface for implementing agents
- **`ClientSideConnection`**: High-level ACP interface for implementing clients
- **`SessionStream`**: Convenience wrapper for sending session updates with minimal boilerplate
- **`Middleware`**: Composable request/response processing chain for cross-cutting concerns
- **`TerminalHandle`**: Resource management wrapper for terminal sessions
- **Generated Types**: Complete type-safe Go structs generated from the official ACP JSON schema

## Quick Start

### Agent

```go
agent := &MyAgent{}
conn := acp.NewAgentSideConnection(agent, os.Stdin, os.Stdout,
    acp.WithMiddleware(acp.RecoveryMiddleware()),
    acp.WithMiddleware(acp.LoggingMiddleware(nil)),
)
conn.Start(context.Background())
```

### Client

```go
client := &MyClient{}
conn, _ := acp.SpawnAgent(ctx, client, "my-agent")
go conn.Start(ctx)

conn.Initialize(ctx, &acp.InitializeRequest{...})
conn.Prompt(ctx, &acp.PromptRequest{...})
```

## Features

### Transport Layer

The SDK supports pluggable transports via the `Transport` interface:

```go
// Default: stdio (newline-delimited JSON)
conn := acp.NewConnection(handler, os.Stdin, os.Stdout)

// HTTP+SSE transport for web deployments
transport := acp.NewHTTPServerTransport()
conn := acp.NewConnection(handler, nil, nil, acp.WithTransport(transport))
http.Handle("/", transport.Handler())
```

### Middleware

Add cross-cutting concerns to the connection handler chain:

```go
conn := acp.NewAgentSideConnection(agent, reader, writer,
    acp.WithMiddleware(
        acp.RecoveryMiddleware(),                   // catch panics
        acp.LoggingMiddleware(log.Printf),          // log method calls
        acp.TimeoutMiddleware(30 * time.Second),    // per-request timeout
    ),
)
```

Custom middleware follows the standard pattern:

```go
func authMiddleware(next acp.MethodHandler) acp.MethodHandler {
    return func(ctx context.Context, method string, params json.RawMessage) (any, error) {
        if method != "initialize" && !isAuthenticated(ctx) {
            return nil, acp.ErrAuthRequired(nil)
        }
        return next(ctx, method, params)
    }
}
```

### SessionStream

Reduce boilerplate when sending session updates from agents:

```go
stream := acp.NewSessionStream(client, sessionID)

// Stream text
stream.SendText(ctx, "Hello!")
stream.SendThought(ctx, "thinking...")

// Tool call lifecycle
stream.StartToolCall(ctx, toolID, "Reading file", acp.ToolKindRead)
stream.CompleteToolCall(ctx, toolID, content...)
stream.FailToolCall(ctx, toolID)

// Other updates
stream.SendPlan(ctx, entries)
stream.SendModeUpdate(ctx, modeID)
stream.SendSessionInfo(ctx, title, updatedAt)
```

### Match Pattern

Exhaustive pattern matching for discriminated union types:

```go
acp.MatchSessionUpdate(&update, acp.SessionUpdateMatcher[string]{
    AgentMessageChunk: func(v acp.SessionUpdateAgentMessageChunk) string {
        return acp.MatchContentBlock(&v.Content, acp.ContentBlockMatcher[string]{
            Text: func(t acp.ContentBlockText) string { return t.Text },
            Default: func() string { return "[non-text]" },
        })
    },
    ToolCall: func(v acp.SessionUpdateToolCall) string {
        return v.Title
    },
    Default: func() string { return "" },
})
```

Matchers are available for all union types: `SessionUpdate`, `ContentBlock`, `ToolCallContent`, `RequestPermissionOutcome`, `MCPServer`.

### Connection Options

```go
acp.NewConnection(handler, reader, writer,
    acp.WithWriteQueueSize(500),                    // configurable write queue
    acp.WithRequestTimeout(30 * time.Second),       // default request timeout
    acp.WithShutdownTimeout(10 * time.Second),      // graceful shutdown timeout
    acp.WithErrorHandler(func(err error) { ... }),   // error callback
)
```

## Protocol Support

This implementation supports ACP Protocol Version 1 with the following features:

### Agent Methods (Client → Agent)
- `initialize` - Initialize the agent and negotiate capabilities
- `authenticate` - Authenticate with the agent (optional)
- `session/new` - Create a new conversation session
- `session/load` - Load an existing session (if supported)
- `session/list` - List available sessions
- `session/set_mode` - Change session mode
- `session/set_config_option` - Update session configuration
- `session/prompt` - Send user prompt to agent
- `session/cancel` - Cancel ongoing operations

### Client Methods (Agent → Client)
- `session/update` - Send session updates (notifications)
- `session/request_permission` - Request user permission for operations
- `fs/read_text_file` - Read text file from client filesystem
- `fs/write_text_file` - Write text file to client filesystem
- **Terminal Support** (unstable):
  - `terminal/create` - Create terminal session
  - `terminal/output` - Get terminal output
  - `terminal/wait_for_exit` - Wait for terminal exit
  - `terminal/kill` - Kill terminal process
  - `terminal/release` - Release terminal handle

### Unstable Features
- `session/fork` - Fork a session (via `SessionForker` interface)
- `session/resume` - Resume a session (via `SessionResumer` interface)
- `session/close` - Close a session (via `SessionCloser` interface)
- `session/set_model` - Set model (via `ModelSetter` interface)

## Contributing

This is an unofficial implementation. For protocol specification changes, please contribute to the [official repository](https://github.com/zed-industries/agent-client-protocol).

For Go implementation issues and improvements, please open an issue or pull request.

## License

This implementation follows the same license as the official ACP specification.

## Related Projects

- **Official ACP Repository**: [zed-industries/agent-client-protocol](https://github.com/zed-industries/agent-client-protocol)
- **Rust Implementation**: Part of the official repository
- **Protocol Documentation**: [agentclientprotocol.com](https://agentclientprotocol.com/)

### Editors with ACP Support

- [Zed](https://zed.dev/docs/ai/external-agents)
- [neovim](https://neovim.io) through the [CodeCompanion](https://github.com/olimorris/codecompanion.nvim) plugin
- [yetone/avante.nvim](https://github.com/yetone/avante.nvim): A Neovim plugin designed to emulate the behaviour of the Cursor AI IDE
