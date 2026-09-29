package acp1

import (
	"cmp"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"sync/atomic"

	acp "github.com/ironpark/acp-go"
	"github.com/ironpark/acp-go/internal/acpconn"
	"github.com/ironpark/acp-go/internal/jsonrpc"
	schema "github.com/ironpark/acp-go/schema/v1"
)

// AgentSideConnection is the agent's view of an ACP connection.
//
// It serves an [Agent] to the peer and implements [Client] for calls back to
// it, so an agent needs no other handle to stream updates, ask for
// permissions, read files or run terminals.
//
// See protocol docs: [Agent](https://agentclientprotocol.com/protocol/overview#agent)
type AgentSideConnection struct {
	conn  *jsonrpc.Connection
	agent Agent
	// clientCaps holds the capabilities from the client's initialize request,
	// once it has been answered.
	clientCaps atomic.Pointer[ClientCapabilities]
}

var _ Client = (*AgentSideConnection)(nil)

// NewAgentSideConnection connects an agent to a client.
//
// newAgent receives the connection being built, so the agent can keep it and
// call the client while handling a request:
//
//	conn := acp1.NewAgentSideConnection(func(c *acp1.AgentSideConnection) acp1.Agent {
//		return &myAgent{client: c}
//	}, acp.NewStdioTransport(os.Stdin, os.Stdout))
//	err := conn.Start(ctx)
//
// transport carries the messages to and from the client; for a stdio agent it
// is [acp.NewStdioTransport] over os.Stdin and os.Stdout.
//
// See protocol docs: [Communication Model](https://agentclientprotocol.com/protocol/overview#communication-model)
func NewAgentSideConnection(newAgent func(*AgentSideConnection) Agent, transport acp.Transport, opts ...acp.Option) *AgentSideConnection {
	c := &AgentSideConnection{}
	c.agent = newAgent(c)
	c.conn = acpconn.NewAgentConnection(c.serveRequest, c.handleNotification, transport, opts)
	return c
}

// agentConnKey carries the [AgentSideConnection] serving a request in the
// request's context, so helpers the agent calls, such as the
// [SessionManager], can reach the client without being handed it.
type agentConnKey struct{}

// agentConnFrom returns the connection serving the request ctx belongs to.
func agentConnFrom(ctx context.Context) (*AgentSideConnection, bool) {
	c, ok := ctx.Value(agentConnKey{}).(*AgentSideConnection)
	return c, ok
}

// serveRequest dispatches a request with the connection in its context, and
// records the client's capabilities once an initialize request succeeds.
func (c *AgentSideConnection) serveRequest(ctx context.Context, method string, params jsontext.Value) (any, error) {
	result, err := c.handleRequest(context.WithValue(ctx, agentConnKey{}, c), method, params)
	if err == nil && method == schema.AgentMethodsInitialize {
		var request InitializeRequest
		if json.Unmarshal(params, &request) == nil {
			c.clientCaps.Store(cmp.Or(request.ClientCapabilities, &ClientCapabilities{}))
		}
	}
	return result, err
}

// ClientCapabilities returns the capabilities the client advertised in its
// initialize request, or nil until the agent has answered it. What the client
// left out it does not support, so an agent checks here before calling an
// optional client method; the returned value is shared, so do not modify it.
func (c *AgentSideConnection) ClientCapabilities() *ClientCapabilities { return c.clientCaps.Load() }

// Client returns the peer client. The connection itself implements [Client].
func (c *AgentSideConnection) Client() Client { return c }

// NewTerminal is CreateTerminal plus a [TerminalHandle] bound to the new
// terminal, which is usually what an agent wants.
func (c *AgentSideConnection) NewTerminal(ctx context.Context, params *CreateTerminalRequest) (*TerminalHandle, error) {
	return newTerminal(ctx, c, params)
}

func newTerminal(ctx context.Context, terminals TerminalHandler, params *CreateTerminalRequest) (*TerminalHandle, error) {
	response, err := terminals.CreateTerminal(ctx, params)
	if err != nil {
		return nil, err
	}
	return NewTerminalHandle(response.TerminalID, params.SessionID, terminals), nil
}
