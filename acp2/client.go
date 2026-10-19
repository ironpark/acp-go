package acp2

import (
	"context"
	"sync"

	acp "github.com/ironpark/acp-go"
	"github.com/ironpark/acp-go/internal/acpconn"
	"github.com/ironpark/acp-go/internal/jsonrpc"
	schema "github.com/ironpark/acp-go/schema/v2"
)

// ClientSideConnection is the client's view of an ACP v2 connection. It serves
// a [Client] to the peer agent and exposes every agent method for outgoing
// calls.
type ClientSideConnection struct {
	conn   *jsonrpc.Connection
	client Client

	mu       sync.Mutex
	sessions map[SessionID]*acpconn.Queue[SessionUpdate] // one per ClientSession
}

var _ Agent = (*ClientSideConnection)(nil)

// NewClientSideConnection connects a client to an agent. newClient receives
// the connection being built so the client can call the agent from its own
// handlers. transport carries the messages to and from the agent; for an
// agent process it is [acp.NewStdioTransport] over its stdout and stdin, which
// [SpawnAgent] sets up.
func NewClientSideConnection(newClient func(*ClientSideConnection) Client, transport acp.Transport, opts ...acp.Option) *ClientSideConnection {
	c := &ClientSideConnection{}
	c.client = newClient(c)
	c.conn = jsonrpc.New(c.handleRequest, c.handleNotification, transport, opts...)
	return c
}

// sessionUpdate queues each session update for the session's [ClientSession],
// if one was created, then hands the update to the client. The generated
// dispatch routes session/update here.
func (c *ClientSideConnection) sessionUpdate(ctx context.Context, n *UpdateSessionNotification) error {
	c.mu.Lock()
	q := c.sessions[n.SessionID]
	c.mu.Unlock()
	if q != nil {
		q.Push(n.Update)
	}
	return c.client.SessionUpdate(ctx, n)
}

// UnimplementedClient provides the methods every [Client] needs for a client
// that only reads its sessions' updates through [ClientSession]: it drops
// session updates, which each ClientSession still queues, and answers
// permission requests with "method not found". Embed it and declare either
// method to override it:
//
//	type myClient struct{ acp2.UnimplementedClient }
//
// session/request_permission is part of every client in the protocol, so
// embed this only for agents that never ask.
type UnimplementedClient struct{}

// SessionUpdate ignores the update.
func (UnimplementedClient) SessionUpdate(context.Context, *UpdateSessionNotification) error {
	return nil
}

// RequestPermission answers "method not found".
func (UnimplementedClient) RequestPermission(context.Context, *RequestPermissionRequest) (*RequestPermissionResponse, error) {
	return nil, acp.MethodNotFound(schema.ClientMethodsSessionRequestPermission)
}

// initialize sends initialize, filling in a zero protocol version. The
// generated [ClientSideConnection.Initialize] goes through it.
func (c *ClientSideConnection) initialize(ctx context.Context, params *InitializeRequest) (*InitializeResponse, error) {
	var request InitializeRequest
	if params != nil {
		request = *params
	}
	if request.ProtocolVersion == 0 {
		request.ProtocolVersion = ProtocolVersion
	}
	return acpconn.Call[InitializeResponse](ctx, c.conn, schema.AgentMethodsInitialize, &request)
}
