package acpmcp

import (
	"context"

	"github.com/ironpark/acp-go/acp1"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// HostV1 serves MCP servers from an ACP v1 client to its agent. It implements
// [acp1.MCPProvider]; embed it in the client so the connection routes the
// agent's mcp/message requests to it:
//
//	type myClient struct {
//		*acpmcp.HostV1
//		// ...
//	}
//
//	acp1.SpawnAgent(ctx, cmd, func(conn *acp1.ClientSideConnection) acp1.Client {
//		return &myClient{HostV1: acpmcp.NewHostV1(conn)}
//	})
type HostV1 struct{ h *host }

// NewHostV1 returns a host that answers the agent on conn.
func NewHostV1(conn *acp1.ClientSideConnection) *HostV1 {
	return &HostV1{h: newHost(func(ctx context.Context, m message) error {
		return conn.NotifyMCP(ctx, &acp1.MessageMCPNotification{
			ServerID: acp1.MCPServerACPID(m.serverID), RequestID: acp1.MCPRequestID(m.requestID), Method: m.method, Params: m.params,
		})
	})}
}

// Add registers server under name and returns its entry for the MCPServers
// of session/new. The agent reaches it only if it advertises
// mcpCapabilities.acp.
func (h *HostV1) Add(name string, server *mcp.Server) acp1.MCPServer {
	return acp1.NewMCPServer(acp1.MCPServerACP{Name: name, ServerID: acp1.MCPServerACPID(h.h.add(server))})
}

func (h *HostV1) MessageMCP(ctx context.Context, params *acp1.MessageMCPRequest) (*acp1.MessageMCPResponse, error) {
	out, err := h.h.message(ctx, message{
		serverID: string(params.ServerID), requestID: string(params.RequestID), method: params.Method, params: params.Params,
	})
	if err != nil {
		return nil, err
	}
	var response acp1.MessageMCPResponse
	if out.err != nil {
		response, err = acp1.NewMessageMCPResponse(acp1.MessageMCPResponseError{Error: acp1.MCPError(toMCPError(out.err))})
	} else {
		response, err = acp1.NewMessageMCPResponse(acp1.MessageMCPResponseResult{Result: out.result})
	}
	return &response, err
}

// DialerV1 connects an ACP v1 agent to the MCP servers its client provides.
// It implements [acp1.MCPMessageHandler], which carries the servers'
// notifications back to the agent; embed it in the agent, and
// [acp1.CapabilitiesOf] then advertises mcpCapabilities.acp:
//
//	type myAgent struct {
//		*acpmcp.DialerV1
//		// ...
//	}
//
//	acp1.NewAgentSideConnection(func(conn *acp1.AgentSideConnection) acp1.Agent {
//		return &myAgent{DialerV1: acpmcp.NewDialerV1(conn)}
//	}, acp.NewStdioTransport(os.Stdin, os.Stdout))
type DialerV1 struct{ d *dialer }

// NewDialerV1 returns a dialer that reaches the client on conn.
func NewDialerV1(conn *acp1.AgentSideConnection) *DialerV1 {
	return &DialerV1{d: newDialer(func(ctx context.Context, m message) (outcome, error) {
		response, err := conn.MessageMCP(ctx, &acp1.MessageMCPRequest{
			ServerID: acp1.MCPServerACPID(m.serverID), RequestID: acp1.MCPRequestID(m.requestID), Method: m.method, Params: m.params,
		})
		if err != nil {
			return outcome{}, err
		}
		// A response with both outcomes is read as its result.
		if result, err := response.As[acp1.MessageMCPResponseResult](); err == nil {
			return outcome{result: result.Result}, nil
		}
		if failed, err := response.As[acp1.MessageMCPResponseError](); err == nil {
			return outcome{err: mcpError(failed.Error).wire()}, nil
		}
		return outcome{}, errNoOutcome
	})}
}

// Connect opens an MCP session with client to server, an entry of the
// MCPServers in session/new, as [mcp.Client.Connect] does over any other
// transport; opts may be nil. The session speaks MCP 2026-07-28, which needs
// no handshake: each of its requests is an mcp/message request of its own.
func (d *DialerV1) Connect(ctx context.Context, server acp1.MCPServerACP, client *mcp.Client, opts *mcp.ClientSessionOptions) (*mcp.ClientSession, error) {
	return d.d.dial(ctx, string(server.ServerID), client, opts)
}

func (d *DialerV1) NotifyMCP(_ context.Context, params *acp1.MessageMCPNotification) error {
	d.d.notify(message{
		serverID: string(params.ServerID), requestID: string(params.RequestID), method: params.Method, params: params.Params,
	})
	return nil
}
