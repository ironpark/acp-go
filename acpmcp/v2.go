package acpmcp

import (
	"context"

	"github.com/ironpark/acp-go/acp2"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// HostV2 serves MCP servers from an ACP v2 client to its agent. It implements
// [acp2.MCPProvider]; embed it in the client so the connection routes the
// agent's mcp/message requests to it:
//
//	type myClient struct {
//		*acpmcp.HostV2
//		// ...
//	}
//
//	acp2.SpawnAgent(ctx, cmd, func(conn *acp2.ClientSideConnection) acp2.Client {
//		return &myClient{HostV2: acpmcp.NewHostV2(conn)}
//	})
type HostV2 struct{ h *host }

// NewHostV2 returns a host that answers the agent on conn.
func NewHostV2(conn *acp2.ClientSideConnection) *HostV2 {
	return &HostV2{h: newHost(func(ctx context.Context, m message) error {
		return conn.NotifyMCP(ctx, &acp2.MessageMCPNotification{
			ServerID: acp2.MCPServerACPID(m.serverID), RequestID: acp2.MCPRequestID(m.requestID), Method: m.method, Params: m.params,
		})
	})}
}

// Add registers server under name and returns its entry for the MCPServers
// of session/new or session/resume. The agent reaches it only if it advertises
// capabilities.session.mcp.acp.
func (h *HostV2) Add(name string, server *mcp.Server) acp2.MCPServer {
	return acp2.NewMCPServer(acp2.MCPServerACP{Name: name, ServerID: acp2.MCPServerACPID(h.h.add(server))})
}

func (h *HostV2) MessageMCP(ctx context.Context, params *acp2.MessageMCPRequest) (*acp2.MessageMCPResponse, error) {
	out, err := h.h.message(ctx, message{
		serverID: string(params.ServerID), requestID: string(params.RequestID), method: params.Method, params: params.Params,
	})
	if err != nil {
		return nil, err
	}
	var response acp2.MessageMCPResponse
	if out.err != nil {
		response, err = acp2.NewMessageMCPResponse(acp2.MessageMCPResponseError{Error: acp2.MCPError(toMCPError(out.err))})
	} else {
		response, err = acp2.NewMessageMCPResponse(acp2.MessageMCPResponseResult{Result: out.result})
	}
	return &response, err
}

// DialerV2 connects an ACP v2 agent to the MCP servers its client provides.
// It implements [acp2.MCPMessageHandler], which carries the servers'
// notifications back to the agent; embed it in the agent, and
// [acp2.CapabilitiesOf] then advertises capabilities.session.mcp.acp:
//
//	type myAgent struct {
//		*acpmcp.DialerV2
//		// ...
//	}
//
//	acp2.NewAgentSideConnection(func(conn *acp2.AgentSideConnection) acp2.Agent {
//		return &myAgent{DialerV2: acpmcp.NewDialerV2(conn)}
//	}, acp.NewStdioTransport(os.Stdin, os.Stdout))
type DialerV2 struct{ d *dialer }

// NewDialerV2 returns a dialer that reaches the client on conn.
func NewDialerV2(conn *acp2.AgentSideConnection) *DialerV2 {
	return &DialerV2{d: newDialer(func(ctx context.Context, m message) (outcome, error) {
		response, err := conn.MessageMCP(ctx, &acp2.MessageMCPRequest{
			ServerID: acp2.MCPServerACPID(m.serverID), RequestID: acp2.MCPRequestID(m.requestID), Method: m.method, Params: m.params,
		})
		if err != nil {
			return outcome{}, err
		}
		// A response with both outcomes is read as its result.
		if result, err := response.As[acp2.MessageMCPResponseResult](); err == nil {
			return outcome{result: result.Result}, nil
		}
		if failed, err := response.As[acp2.MessageMCPResponseError](); err == nil {
			return outcome{err: mcpError(failed.Error).wire()}, nil
		}
		return outcome{}, errNoOutcome
	})}
}

// Connect opens an MCP session with client to server, an entry of the
// MCPServers in session setup, as [mcp.Client.Connect] does over any other
// transport; opts may be nil. The session speaks MCP 2026-07-28, which needs
// no handshake: each of its requests is an mcp/message request of its own.
func (d *DialerV2) Connect(ctx context.Context, server acp2.MCPServerACP, client *mcp.Client, opts *mcp.ClientSessionOptions) (*mcp.ClientSession, error) {
	return d.d.dial(ctx, string(server.ServerID), client, opts)
}

func (d *DialerV2) NotifyMCP(_ context.Context, params *acp2.MessageMCPNotification) error {
	d.d.notify(message{
		serverID: string(params.ServerID), requestID: string(params.RequestID), method: params.Method, params: params.Params,
	})
	return nil
}
