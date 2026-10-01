package acp2

import (
	"cmp"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"sync/atomic"

	acp "github.com/ironpark/acp-go"
	"github.com/ironpark/acp-go/internal/acpconn"
	"github.com/ironpark/acp-go/internal/jsonrpc"
	schema "github.com/ironpark/acp-go/schema/v2"
)

// AgentSideConnection is the agent's view of an ACP v2 connection. It serves
// an [Agent] to the peer and implements [Client] for calls back to it.
type AgentSideConnection struct {
	conn  *jsonrpc.Connection
	agent Agent
	// clientCaps holds the capabilities from the client's initialize request,
	// once it has been answered.
	clientCaps atomic.Pointer[ClientCapabilities]
}

var _ Client = (*AgentSideConnection)(nil)

// NewAgentSideConnection connects an agent to a client. newAgent receives the
// connection being built so the agent can keep it as its [Client]. transport
// carries the messages to and from the client, such as
// [acp.NewStdioTransport] over os.Stdin and os.Stdout.
func NewAgentSideConnection(newAgent func(*AgentSideConnection) Agent, transport acp.Transport, opts ...acp.Option) *AgentSideConnection {
	c := &AgentSideConnection{}
	c.agent = newAgent(c)
	c.conn = acpconn.NewAgentConnection(c.serveRequest, c.handleNotification, transport, opts)
	return c
}

// serveRequest dispatches a request, and records the client's capabilities
// once an initialize request succeeds.
func (c *AgentSideConnection) serveRequest(ctx context.Context, method string, params jsontext.Value) (any, error) {
	result, err := c.handleRequest(ctx, method, params)
	if err == nil && method == schema.AgentMethodsInitialize {
		var request InitializeRequest
		if json.Unmarshal(params, &request) == nil {
			c.clientCaps.Store(cmp.Or(request.Capabilities, &ClientCapabilities{}))
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
