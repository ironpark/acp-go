# ACP Go Examples

English | [한국어](./README.ko.md)

Runnable examples for the [ACP Go SDK](https://github.com/ironpark/acp-go), from the smallest agent to a
different transport. Run each from the repository root.

| Example | Shows | Run |
|---|---|---|
| [`echo`](./echo/main.go) | The smallest agent: the four required methods, streaming each prompt back | `go run ./examples/echo` |
| [`agent`](./agent/) | A complete agent: `SessionManager` sessions and cancellation, session modes, a plan, `SessionStream` tool calls with a command run in the client's terminal and a file diff, a permission request, an `ExtRouter` extension method, logging middleware; tests with `acp1test` | `go run ./examples/agent` |
| [`client`](./client/) | An interactive client for any stdio agent: `SpawnAgent`, `ClientSession`/`Turn`, rendering updates, plans and diffs, Ctrl-C cancellation, permission prompts, `/mode` switching, file system and terminal methods, `CallExt` | `go run ./examples/client [agent command...]` |
| [`open-agent`](./open-agent/) | A coding agent driven by a model on [OpenRouter](https://openrouter.ai): streamed answers and reasoning, a tool-calling loop whose tools read and write files through the client and run commands in its terminal, permission in ask mode, usage and cost reports, sessions saved to disk with `FileStore`; the model client uses only the standard library | `OPENROUTER_API_KEY=... go run ./examples/open-agent` |
| [`http-agent`](./http-agent/main.go) | The echo agent served over Streamable HTTP and WebSocket on one endpoint with `acphttp.Server`, with sessions that outlive a connection, an optional bearer token and a graceful shutdown on Ctrl-C | `go run ./examples/http-agent [-token secret]` |
| [`http-client`](./http-client/main.go) | One prompt turn against `http-agent` with `ConnectAgent`, over Streamable HTTP or, with `-ws`, WebSocket; `-reconnect` then resumes the session with `session/load` | `go run ./examples/http-client [-ws] [-reconnect] [-token secret]` |
| [`dual-agent`](./dual-agent/) | One binary serving ACP v1 and the draft v2 through `router.ProtocolRouter`, including the v2 prompt lifecycle and v2 session resume with history replay; each version's agent in its own file | `go run ./examples/dual-agent` |
| [`dual-client`](./dual-client/) | `router.ClientConnector`: v2 when the agent supports it, v1 otherwise; on v2 it closes the session and resumes it with a replay | `go run ./examples/dual-client [agent command...]` |
| [`inprocess`](./inprocess/main.go) | An agent and a client in one process, connected in memory with `acp1.Pipe` | `go run ./examples/inprocess` |
| [`acpmcp/example`](../acpmcp/example/main.go) | **Unstable.** MCP-over-ACP: the client provides an MCP server the agent calls over the ACP connection, with the [`acpmcp`](../acpmcp/) module | `cd acpmcp && go run ./example` |

## Agent and client together

With no arguments, the client builds the `agent` example and talks to it:

```sh
go run ./examples/client
```

Type a message to start a turn: the agent shows its plan, runs `go version` in a terminal the client provides,
and asks before applying a diff. Press Ctrl-C to cancel a running turn, send `/mode auto` to let the agent edit
without asking (`/mode ask` switches back), or `/ping hello` to call the agent's `_example.com/ping` extension
method. Ctrl-C while the agent asks for permission cancels the turn and the question. Ctrl-D quits. Pass `-v`
to see the agent's logs.

Any other stdio agent works too; give its command after the flags:

```sh
go build -o /tmp/echo ./examples/echo
go run ./examples/client /tmp/echo
```

## With a model on OpenRouter

`open-agent` hands its turns to a model on [OpenRouter](https://openrouter.ai). The model reads files, writes them,
and runs commands through the client:

```sh
export OPENROUTER_API_KEY=sk-or-...
go build -o /tmp/open-agent ./examples/open-agent
go run ./examples/client /tmp/open-agent
```

Ask it about the project, for example "what does this module depend on?". In `ask` mode it asks before each write
and each command; `/mode auto` lets it go ahead. It reads its configuration from the environment:

| Variable | Default |
|---|---|
| `OPENROUTER_API_KEY` | required, from [openrouter.ai/keys](https://openrouter.ai/keys) |
| `OPENROUTER_MODEL` | `openai/gpt-oss-120b`; any OpenRouter model that supports tools |
| `OPENROUTER_BASE_URL` | `https://openrouter.ai/api/v1`; any OpenAI-compatible endpoint |
| `OPEN_AGENT_SESSIONS` | `open-agent/sessions` in the user cache directory; sessions are saved there so a client can list, load and resume them after a restart |

In Zed, give it the key through `env`:

```json
  "agent_servers": {
    "Open Agent": {
      "command": "go",
      "args": ["run", "-C", "/path/to/acp-go/examples/open-agent", "."],
      "env": { "OPENROUTER_API_KEY": "sk-or-..." }
    }
  }
```

## Over HTTP

The agent speaks Streamable HTTP, the remote transport of the TypeScript and Python SDKs, so their clients work
with it too. Start the agent, then run the client in another terminal:

```sh
go run ./examples/http-agent
go run ./examples/http-client      # Streamable HTTP
go run ./examples/http-client -ws  # WebSocket
go run ./examples/http-client -reconnect  # drop the connection, then load the session
```

`http-agent -token secret` accepts only clients that send `Authorization: Bearer secret`, as
`http-client -token secret` does. Authentication is ordinary `http.Handler` middleware in front of `acphttp.Server`.

Ctrl-C stops the agent gracefully: `acphttp.Server.Shutdown` refuses new connections and gives the open ones a few
seconds to end, then the `http.Server` shuts down. That order matters; the open connections still need the HTTP
server to finish.

## v1 and v2 together

With no arguments, the dual client builds `dual-agent` and negotiates v2. Point it at a v1-only agent and it
restarts the agent with v1:

```sh
go run ./examples/dual-client            # negotiated v2
go build -o /tmp/echo ./examples/echo
go run ./examples/dual-client /tmp/echo  # negotiated v1
```

The v1 `client` example works against `dual-agent` too; the router gives it the v1 agent.

## Agent by itself

An agent reads JSON-RPC from stdin and writes to stdout, so you can drive one by hand:

```sh
go run ./examples/agent
```

Paste this and press <kbd>enter</kbd>:

```json
{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":1}}
```

It answers with its capabilities (its request log goes to stderr):

```json
{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":1,"agentCapabilities":{"sessionCapabilities":{"delete":{},"resume":{},"close":{}}},"agentInfo":{"name":"example-agent","version":"0.1.0"}}}
```

From there, try [creating a session](https://agentclientprotocol.com/protocol/session-setup#creating-a-session) and
[sending a prompt](https://agentclientprotocol.com/protocol/prompt-turn#1-user-message).

## In Zed

[`agent/main.go`](./agent/main.go) is a compliant [ACP](https://agentclientprotocol.com) agent, so an ACP client like [Zed](https://zed.dev) can connect to it. The `echo` example works the same way; point the path at `examples/echo` instead.

1. Clone this repo

```sh
$ git clone https://github.com/ironpark/acp-go.git
```

2. Add the following at the root of your [Zed](https://zed.dev) settings:
> [!NOTE]
> Run the `agent: open settings` action from the command palette (<kbd>⌘⇧P</kbd> on macOS, <kbd>ctrl-shift-p</kbd> on Windows/Linux) 
```json
  "agent_servers": {
    "Example Agent": {
      "command": "go",
      "args": [
        "run",
        "-C",
        "/path/to/acp-go/examples/agent",
        "."
      ],
      "env": {}
    }
  }
```

> [!NOTE]
>  Make sure to replace `/path/to/acp-go/examples/agent` with the path to your clone of this repository.


3. Run the `dev: open acp logs` action from the command palette (<kbd>⌘⇧P</kbd> on macOS, <kbd>ctrl-shift-p</kbd> on Windows/Linux) to see the messages exchanged between the example agent and Zed.

4. Then open the Agent Panel, and click "New Example Agent Thread" from the `+` menu on the top-right.

![Agent menu](../docs/imgs/menu.png)

5. Finally, send a message and see the Agent respond!

![Final state](../docs/imgs/final.png)

## MCP over ACP (unstable)

MCP-over-ACP is an RFD-stage draft of the protocol, not yet stable: the TypeScript and Python SDKs mark it
unstable, the Rust SDK hides it behind a feature flag, and its wire format may still change. The
[`acpmcp`](../acpmcp/) module connects it to the MCP Go SDK, and lives in its own module so the SDK does not
depend on MCP. Its example runs from that module:

```sh
cd acpmcp && go run ./example
```
