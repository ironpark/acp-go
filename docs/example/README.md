# ACP Go Examples

This directory contains examples using the [ACP Go SDK](https://github.com/ironpark/acp-go):

- [`agent/main.go`](./agent/main.go) - Agent implementation demonstrating SessionStream, middleware, tool calls, and permission requests
- [`client/main.go`](./client/main.go) - Client implementation using SpawnAgent and MatchSessionUpdate for type-safe update handling

## Running the Agent

### In Zed

While minimal, [`agent/main.go`](./agent/main.go) implements a compliant [ACP](https://agentclientprotocol.com) Agent. This means we can connect to it from an ACP client like [Zed](https://zed.dev)!

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
        "/path/to/go-acp/docs/example/agent",
        "."
      ],
      "env": {}
  }
```

> [!NOTE]
>  Make sure to replace `/path/to/go-acp/docs/example/agent` with the path to your clone of this repository.


3. Run the `dev: open acp logs` action from the command palette (<kbd>⌘⇧P</kbd> on macOS, <kbd>ctrl-shift-p</kbd> on Windows/Linux) to see the messages exchanged between the example agent and Zed.

4. Then open the Agent Panel, and click "New Example Agent Thread" from the `+` menu on the top-right.

![Agent menu](../imgs/menu.png)

5. Finally, send a message and see the Agent respond!

![Final state](../imgs/final.png)

### By itself

You can also run the Agent directly and send messages to it:

```bash
go run ./docs/example/agent
```

Paste this into your terminal and press <kbd>enter</kbd>:

```json
{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":1}}
```

You should see it respond with something like:

```json
{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":1,"agentCapabilities":{"loadSession":false}}}
```

From there, you can try making a [new session](https://agentclientprotocol.com/protocol/session-setup#creating-a-session) and [sending a prompt](https://agentclientprotocol.com/protocol/prompt-turn#1-user-message).
