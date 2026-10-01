# SDK Guide

[README](../README.md) | English | [한국어](guide.ko.md)

The snippets below illustrate individual APIs; complete runnable programs are in [examples](../examples/README.md).


Examples use `acp1` unless stated otherwise. Application types and values such as `MyAgent`, `MyClient`, and `ctx` are supplied by your application.

- [Architecture](#architecture)
- [Implementing an Agent and Client](#implementing-an-agent-and-client)
- [Sessions and Updates](#sessions-and-updates)
- [Connections and Versions](#connections-and-versions)
- [Errors and Middleware](#errors-and-middleware)
- [Data and Extensions](#data-and-extensions)

## Architecture

| Component | Role |
| --- | --- |
| `acp` (root) | `Option`s, `Transport` and the stdio transport, `Middleware`, `RequestError`, `SessionStore` (`MemoryStore`, `FileStore`), `TurnTracker`, typed extensions (`CallExt`, `ExtRouter`, `ExtMethodHandler`) |
| `acphttp` | Streamable HTTP and WebSocket transports, following the draft RFD, apart from the root so stdio programs do not link them |
| `acp1.AgentSideConnection` | serves an `Agent` and calls the peer client |
| `acp1.ClientSideConnection` | serves a `Client` and calls the peer agent |
| `acp1.SpawnAgent`, `acp1.Pipe` | an agent as a child process, or both sides in memory |
| `acp1.ClientSession`, `acp1.Turn` | prompt a session and read that turn's updates |
| `acp1.SessionManager` | session lifecycle and turn cancellation backed by a store |
| `acp1.SessionStream` | session updates without rebuilding the union by hand |
| `acp1.TerminalHandle` | terminal id and session id bound together |
| `acp1.CapabilitiesOf` | capabilities derived from the interfaces an agent implements |
| `acp2` | the same façades for the draft ACP v2 (`schema/v2`) |
| `router.ProtocolRouter` | one endpoint serving v1 and v2 agents; **`router.ClientConnector`** — the client side, v2 with v1 fallback |
| `acpmcp` (separate module, unstable) | MCP-over-ACP on top of the MCP Go SDK: `HostV1`/`HostV2` serve a client's MCP servers, `DialerV1`/`DialerV2` connect an agent to them |
| `schema/v1`, `schema/v2` | generated wire types, unions and Zod-based validation |

Incoming parameters are validated with the SDK's own Zod rules before a handler sees them,
and invalid ones are answered with `-32602` without invoking the handler.

## Implementing an Agent and Client

### Agent

```go
conn := acp1.NewAgentSideConnection(func(c *acp1.AgentSideConnection) acp1.Agent {
    return &MyAgent{client: c} // the connection is also the peer Client
}, acp.NewStdioTransport(os.Stdin, os.Stdout))

if err := conn.Start(context.Background()); err != nil {
    log.Fatal(err)
}
```

`Agent` requires only `Initialize`, `NewSession`, `Prompt` and `CancelSession`.
Everything else is an optional interface — implement `acp1.Authenticator`, `acp1.SessionLoader`, `acp1.SessionLister`,
`acp1.SessionModeSetter`, `acp1.NesHandler` and so on. Methods you do not implement are answered
with `-32601`. `acp1.CapabilitiesOf(agent)` returns the capabilities those interfaces imply, so the
`Initialize` response cannot advertise a method the connection would reject:

```go
caps := acp1.CapabilitiesOf(a)
caps.PromptCapabilities = &schema.PromptCapabilities{Image: new(true)} // content capabilities are yours to set
return &acp1.InitializeResponse{ProtocolVersion: acp1.ProtocolVersion, AgentCapabilities: caps}, nil
```

Once `Initialize` is answered, `conn.ClientCapabilities()` returns what the client advertised, so the
agent need not keep its own copy. The v1 `SessionStream`'s `ReadTextFile`, `WriteTextFile` and
`NewTerminal` check it and fail with an error matching `errors.ErrUnsupported`, without a request,
when the client left the capability out:

```go
if a.conn.ClientCapabilities().GetTerminal() {
    // run the command in the editor's terminal
}
```

### Client

```go
client := &MyClient{}
agent, err := acp1.SpawnAgent(ctx, exec.Command("my-agent"), func(*acp1.ClientSideConnection) acp1.Client {
    return client
})
if err != nil {
    log.Fatal(err)
}
defer agent.Close()

if _, err := agent.Initialize(ctx, &acp1.InitializeRequest{ // a zero ProtocolVersion sends acp1.ProtocolVersion
    ClientCapabilities: acp1.ClientCapabilitiesOf(client),
}); err != nil {
    log.Fatal(err)
}
session, err := agent.StartSession(ctx, &acp1.NewSessionRequest{Cwd: cwd})
if err != nil {
    log.Fatal(err)
}

turn, err := session.Prompt(ctx, acp1.TextBlock("Summarize README.md"))
if err != nil {
    log.Fatal(err)
}
for update := range turn.Updates() {
    render(update) // tool calls, plans, message chunks...
}
response, err := turn.Wait() // or: text, response, err := turn.Text()
if err != nil {
    log.Fatal(err)
}
```

`SpawnAgent` already runs the read loop; `agent.Wait()` reports how the process and connection
ended. The agent's stderr goes to the parent's unless `cmd.Stderr` is set. `acp1.Pipe` connects
an agent and a client in memory, which is handy in tests; `acp1test.Connect` wraps it for a test, and
`acp1test.Client` records the updates and permission requests an agent sends (`Updates`, `Text`,
`WaitFor`) and answers the requests with `acp1test.AllowOnce`, `AllowAlways`, `Reject` or a function
of your own. `acp2test` does the same for v2.

`Client` requires only `SessionUpdate` and `RequestPermission`; a client whose agent never asks for
permission can embed `acp1.UnimplementedClient` for both.

`SessionUpdate` sees every update,
including those outside a `Turn`. Updates are handled one at a time, in order, and before the prompt
response and any agent request sent after them; a slow handler does not stop the connection from
reading. A handler may call the agent and wait for the answer, as long as the agent does not need to
call back into the client to produce it.

File system, terminal and elicitation support come from
`acp1.FileReader`, `acp1.FileWriter`, `acp1.TerminalHandler` and `acp1.ElicitationHandler`;
`acp1.ClientCapabilitiesOf(client)` derives the matching flags.

## Sessions and Updates

### Sessions

```go
manager := acp1.NewSessionManager(
    acp1.NewMemoryStore[*MySession](),
    func(ctx context.Context, params *acp1.NewSessionRequest) (acp1.SessionID, *MySession, error) {
        return acp1.GenerateSessionID(), &MySession{cwd: params.Cwd}, nil
    },
)

type MyAgent struct {
    *acp1.SessionManager[*MySession] // NewSession, CancelSession, DeleteSession, ResumeSession, CloseSession
}

// Optional: session/new and session/resume report the modes; List describes the session.
func (s *MySession) SessionModes() *acp1.SessionModeState {
    return &acp1.SessionModeState{CurrentModeID: s.mode, AvailableModes: myModes}
}
func (s *MySession) SessionInfo() acp1.SessionInfo { return acp1.SessionInfo{Cwd: s.cwd} }

func (a *MyAgent) ListSessions(ctx context.Context, params *acp1.ListSessionsRequest) (*acp1.ListSessionsResponse, error) {
    return a.List(ctx, params) // cwd filter, most recently updated first
}

// RunTurn looks the session up and runs one turn: the manager's CancelSession cancels
// ctx and the turn is answered as cancelled; a second prompt meanwhile gets
// acp.ErrTurnInProgress, since v1 runs one turn per session.
func (a *MyAgent) Prompt(ctx context.Context, params *acp1.PromptRequest) (*acp1.PromptResponse, error) {
    return a.RunTurn(ctx, params.SessionID, func(ctx context.Context, s *MySession) (acp1.StopReason, error) {
        return acp1.StopReasonEndTurn, a.work(ctx, s)
    })
}

// RunTurnResponse is the same for a response with more than a stop reason, such as usage;
// a cancelled turn keeps the response's other fields.
return a.RunTurnResponse(ctx, params.SessionID, func(ctx context.Context, s *MySession) (*acp1.PromptResponse, error) {
    reason, err := a.work(ctx, s)
    return &acp1.PromptResponse{StopReason: reason, Usage: s.usage()}, err
})
```

Override any of those by declaring the method on the agent itself. Session state that implements
`SessionModesReporter` or `SessionConfigOptionsReporter` has its modes and config options reported
in the session/new and session/resume responses. State that implements `SessionCommandsReporter`
has its slash commands sent in an `available_commands_update` right after those responses in v1, so
the update never reaches the client before the session it names; v2 returns them in the responses'
`availableCommands` instead. `acp.TurnCancelled(ctx)` tells a
turn cancelled by `session/cancel` apart from other cancellation.

The manager leaves out `session/load`, which must
replay a conversation only the agent knows, and, in v1, the optional `session/list`: an agent that
lists forwards `ListSessions` to `List`, which describes each session with its state's
`SessionInfoReporter`. `List` paginates: it returns at most 100 sessions per page and hands back a
next cursor while more remain; set the page size with `acp1.WithSessionListPageSize` (zero returns
every match in one page).

By default each page reads and describes every stored session. A store backed by a database can
implement `acp1.SessionInfoLister` (`acp.SessionInfoLister` for the façade's `SessionInfo`) to answer
a page with one query instead: `ListSessionInfo` receives an `acp.SessionListQuery` (cwd filter,
the position to start after, and a limit one past the page size) and returns matching sessions in
`acp.SessionListPosition.Compare` order — newest `updatedAt` first, compared as strings, undated
sessions last, ties by session id — with each `SessionID` set.

| Method | Behavior |
| --- | --- |
| `Lookup(ctx, id)` | Return the session state or an error to pass back to the caller. |
| `CloseSession` | Cancel the running turn and keep the session available to resume. |
| `DeleteSession` | Remove the session; deleting an unknown session also succeeds. |

`acp2.SessionManager` serves the whole v2 session
baseline, list included, the same way. `acp.SessionStore[ID, T]`, whose methods take the request's
context and can fail, `acp.MemoryStore` and `acp.TurnTracker` are the version-neutral building
blocks; each façade aliases the stores with its own session id.

`acp.FileStore` (`acp1.NewFileStore[T](dir)`) keeps sessions across restarts: it serves `Get` and
`List` from memory like `MemoryStore`, so state can still change in place, and writes each session
to its own JSON file in `dir` on `Set`. The manager sets a session when it creates it, and with
`acp1.WithAutoSave(onError)` also whenever a turn begun on it ends, cancelled or not; save changes
made outside a turn, such as by `session/set_mode`, with `manager.Save(ctx, id, session)`, which
ignores ctx's cancellation.
Sessions encode with `encoding/json/v2`, which skips unexported fields; session state with
unexported fields implements `MarshalJSON`/`UnmarshalJSON`. One process at a time may use a
directory.

### SessionStream

```go
stream := acp1.NewSessionStream(client, sessionID)

stream.SendText(ctx, "Hello!")
stream.SendThought(ctx, "thinking...")

stream.StartToolCall(ctx, toolID, "Reading file", acp1.ToolKindRead)
stream.CompleteToolCall(ctx, toolID, acp1.WithToolContent(acp1.ToolText(contents)))
stream.ProposeToolCall(ctx, runID, "Run tests", acp1.ToolKindExecute) // pending: awaiting permission
stream.CompleteToolCall(ctx, editID, acp1.WithToolContent(acp1.ToolDiff(path, &oldText, newText)))
stream.CompleteToolCall(ctx, runID, acp1.WithToolContent(acp1.ToolTerminal(terminal.ID))) // terminal from conn.NewTerminal

stream.SendPlan(ctx, entries)
stream.Send(ctx, acp1.SessionUpdateSessionInfoUpdate{Title: new("Refactor")}) // variants without a helper
stream.WithMeta(meta).SendText(ctx, "…")                                       // _meta on each notification
```

`acp1.TextBlock`, `acp1.TextOf`, `acp1.Texts` (an iterator over a prompt's text blocks),
`acp1.JoinTexts` (their concatenation) and
`acp1.ToolText` cover the common text content, and `acp1.ToolDiff` and `acp1.ToolTerminal` the
other tool output.

`stream.RunTerminal(ctx, toolID, request, timeout)` runs a command in a client terminal from start
to finish: it shows the terminal in the tool call, waits for the exit, kills the command on the
timeout or a cancelled ctx, and returns the output and exit status before releasing the terminal.

For config options, `acp1.SelectOptions(choices...)` builds a select option's choices without the
error `NewSessionConfigSelectOptions` returns, and `acp1.ConfigChangeOf(params)` reads a
`session/set_config_option` request as one `ConfigChange` whichever variant it holds.

To send a notification that must follow a response, such as an update about a session the
response creates, register it with `acp.AfterReply(ctx, fn)` in the handler: `fn` runs once the
response is queued, and the connection writes in order.

Tool call ids must be unique within a session; `acp1.GenerateToolCallID` and
`acp1.GenerateMessageID` mint random ones, like `GenerateSessionID`. The v2
`SessionStream` takes a message id on every message and adds `Running`, `RequiresAction`, `Idle`
and `Unknown` for the explicit turn state.

An agent that delegates work to child sessions can expose them as subagents (unstable).
`stream.StartSubagent(ctx, childID, title, opts...)` announces the child on the parent session and
returns a `SubagentStream`: its embedded `SessionStream` reports the child's own updates, and its
`Running`, `RequiresAction`, `Idle` and `Unknown` report the child's work — on the parent's
`subagent_update` in v1, and on the child's stream mirrored to the parent in v2. In v1 it fails
with `errors.ErrUnsupported` unless the client advertised the `subagents` capability.

### Cancellation

Cancelling the context of an outgoing call sends `$/cancel_request` for that request id.
On the receiving side the matching handler's context is cancelled, and the peer gets
`-32800 Request cancelled` unless the handler answers first. This is separate from
`session/cancel`, which cancels a whole prompt turn (see [Sessions](#sessions)).

## Connections and Versions

### Transport Layer

```go
// Default: stdio (newline-delimited JSON)
conn := acp1.NewAgentSideConnection(newAgent, acp.NewStdioTransport(os.Stdin, os.Stdout))

// Streamable HTTP and WebSocket for remote agents: one connection, and one agent, per client
server := acphttp.NewServer(func(ctx context.Context, t acp.Transport) error {
    return acp1.NewAgentSideConnection(newAgent, t).Start(ctx)
})
http.Handle("/acp", server)

// The client side of any transport
agent := acp1.ConnectAgent(ctx, acphttp.NewClientTransport("https://host/acp"), newClient)
defer agent.Close() // also ends the connection on the server

ws, err := acphttp.DialWebSocket(ctx, "wss://host/acp") // the same endpoint over WebSocket
agent := acp1.ConnectAgent(ctx, ws, newClient)
```

Both follow the draft RFD the TypeScript and Python SDKs implement, and interoperate with them.
Streamable HTTP uses `POST` for client messages (`initialize` answers with an
`Acp-Connection-Id`) and Server-Sent Events streams for the agent's, one per connection and one
per session. A `GET` with `Upgrade: websocket` on the same endpoint carries the whole connection
as text frames instead. WebSockets from browser pages on other origins are refused unless
`acphttp.WithWebSocketOrigins` allows them.

`acphttp.Server` is an ordinary `http.Handler`, so authentication is middleware in front of it, and
the context `serve` receives keeps the values of the request that opened the connection: what the
middleware stored there, such as the user, reaches the agent. `WithErrorHandler` receives the errors
`serve` returns, and `Server.Shutdown(ctx)` refuses new connections and waits for the open ones to
end before closing them. Call it before the `http.Server`'s own `Shutdown`, which would stop the open
connections' POSTs and wait out its deadline on their event streams; `examples/http-agent` does both on
Ctrl-C. On the client, a refused request fails with an `*acphttp.StatusError`
carrying the HTTP status, such as 401 when credentials have expired.

#### Reconnecting

Reconnecting creates a new connection: dial again with the same headers and
`acphttp.WithCookieJar(jar)`, so a load balancer's affinity cookie routes the client back, then
`Initialize` and `LoadSession` the saved session id if the agent advertises `loadSession`.
Messages sent while the client was away are not replayed; the protocol leaves that to v2.
The `http-client` example shows the flow with `-reconnect`.

#### Temporary disconnections

The transport handles short drops automatically. The HTTP client reopens a dropped event stream, backing off,
until the server answers that the connection is gone; the server hands a stream to the newer `GET`
and resends a message whose write failed. The server ends a connection whose client has had no
stream open for five minutes (`acphttp.WithIdleTimeout`), and both ends of a WebSocket ping every 15
seconds and close it when the peer stops answering (`acphttp.WithWebSocketPing` on the server,
`acphttp.WithPingInterval` on the client).

### Serving v1 and v2 together

```go
r := router.New().
    WithV1(func(c *acp1.AgentSideConnection) acp1.Agent { return &v1Agent{client: c} }).
    WithV2(func(c *acp2.AgentSideConnection) acp2.Agent { return &v2Agent{client: c} })
err := r.Serve(ctx, acp.NewStdioTransport(os.Stdin, os.Stdout))
```

The router reads the first message, which must be `initialize`, picks the highest configured
version not above the one requested, rewrites only the initialize params (a v2 request routed to
a v1-only agent is downgraded: `info` → `clientInfo`, no `fs`/`terminal`), and forwards everything
after that unchanged. Options, transports and middleware live in the root `acp` package, so one
value configures either façade.

A client that supports both versions uses `router.NewClient`. It spawns the agent, initializes
with v2, and if the agent answers `protocolVersion` 1 (or rejects the v2 request) restarts it
with v1, so each version sends its own initialize request:

```go
agent, err := router.NewClient().
    WithV1(newV1Client, &acp1.InitializeRequest{ClientCapabilities: v1Caps}).
    WithV2(newV2Client, &acp2.InitializeRequest{Info: info}).
    Spawn(ctx, func() *exec.Cmd { return exec.Command("my-agent") })
if err != nil {
    log.Fatal(err)
}
defer agent.Close() // Close, Wait, Done and extension calls work on either version
if agent.V2 != nil {
    // agent.V2, agent.V2Init
} else {
    // agent.V1, agent.V1Init
}
```

For a remote agent, `Connect` takes a dial function instead, called once per attempt:

```go
agent, err := router.NewClient().WithV1(…).WithV2(…).
    Connect(ctx, func(ctx context.Context) (acp.Transport, error) {
        return acphttp.NewClientTransport("https://host/acp"), nil // or acphttp.DialWebSocket
    })
```

### Connection Options

```go
acp1.NewAgentSideConnection(newAgent, acp.NewStdioTransport(os.Stdin, os.Stdout),
    acp.WithWriteQueueSize(500),               // outgoing queue depth
    acp.WithRequestTimeout(30*time.Second),    // default deadline for outgoing calls
    acp.WithShutdownTimeout(10*time.Second),   // bound Close on in-flight handlers
    acp.WithErrorHandler(func(err error) {}),  // non-fatal errors
)
```

## Errors and Middleware

### Errors

Return an `acp.Err…` constructor from a handler to choose the code the peer receives; any other
error becomes `-32603`. Errors from the peer come back as `*acp.RequestError`:

```go
if acp.IsCode(err, acp.ErrorCodeAuthRequired) {
    // authenticate, then retry
}
```

### Middleware

```go
conn := acp1.NewAgentSideConnection(newAgent, acp.NewStdioTransport(os.Stdin, os.Stdout),
    acp.WithMiddleware(
        acp.LoggingMiddleware(slog.Default()),   // log methods, durations and errors
        acp.TimeoutMiddleware(30*time.Second),   // per-handler timeout
    ),
)
```

`LoggingMiddleware` logs requests at Info and notifications, such as the frequent session
updates, at Debug, with `method` and `duration` attributes; a failure logs at Warn with `error`
and the JSON-RPC `code`.

`MetricsMiddleware` reports every handled message to an `acp.Metrics` implementation with its
method, kind (`acp.RPCRequest` or `acp.RPCNotification`), duration and error, so one hook covers
counters, histograms and tracing:

```go
acp.MetricsMiddleware(acp.MetricsFunc(func(ctx context.Context, method string, kind acp.RPCKind, d time.Duration, err error) {
    rpcCalls.WithLabelValues(method, string(kind)).Inc()
    rpcDuration.WithLabelValues(method).Observe(d.Seconds())
}))
```

Panics in handlers are already recovered by the connection and reported as `-32603`,
so no recovery middleware is needed. Custom middleware wraps either direction:

```go
authenticated := acp.Middleware{
    Request: func(next acp.RequestHandler) acp.RequestHandler {
        return func(ctx context.Context, method string, params jsontext.Value) (any, error) {
            if method != schema.AgentMethodsInitialize && !isAuthenticated(ctx) {
                return nil, acp.AuthRequired("")
            }
            return next(ctx, method, params)
        }
    },
}
```

## Data and Extensions

### Unions

Generated unions wrap a sealed variant interface; read one with a type switch:

```go
switch update := notification.Update.Variant().(type) {
case acp1.SessionUpdateAgentMessageChunk:
    if text, ok := acp1.TextOf(update.Content); ok {
        fmt.Print(text)
    }
case acp1.SessionUpdateToolCall:
    fmt.Println(update.Title)
}

update := acp1.NewSessionUpdate(acp1.SessionUpdatePlan{Entries: entries})
```

A tag this SDK does not know never fails the message. It decodes into the union's
`Custom` variant where the schema defines one, and otherwise into a generated `…Unknown`
variant (such as `acp1.SessionUpdateUnknown`) whose `Raw` field holds the object as
received and is encoded unchanged. Handle it in a `default` case, or ignore it as the
protocol recommends.

### Optional Fields

Optional fields are pointers (`omitzero`), so an explicit `false` or `""` survives encoding. Every
pointer field also has a nil-safe getter, protobuf style: a pointer to a struct comes back as is, so
calls chain through absent objects, and any other pointer is dereferenced, giving the zero value when
the field or the receiver is nil:

```go
if init.GetAgentCapabilities().GetMCPCapabilities().GetACP() { ... }
title := params.ToolCall.GetTitle() // "" when absent
```

Read the field itself when absence means something the zero value does not: a terminal's `nil`
`ExitCode` means a signal ended it, not exit code 0.

### Extensions and `_meta`

```go
type MyAgent struct {
    acp.ExtRouter // implements acp.ExtMethodHandler and acp.ExtNotificationHandler
    // ...
}

a.HandleExt("_example.com/index", func(ctx context.Context, p *IndexParams) (*IndexResult, error) {
    return &IndexResult{Files: 42}, nil
})

result, err := acp.CallExt[IndexResult](ctx, conn, "_example.com/index", IndexParams{Path: "."})
```

Params that fail to decode get `-32602`, unregistered methods `-32601`, and unregistered
notifications are ignored. Every `_meta` field is an `acp1.Meta`, which keeps values as raw JSON:

```go
var meta acp1.Meta
meta.Set("trace", Trace{ID: "abc"})
trace, ok, err := params.Meta.Get[Trace]("trace")
```
