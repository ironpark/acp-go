package acp1

import (
	"context"

	acp "github.com/ironpark/acp-go"
	"github.com/ironpark/acp-go/internal/acpconn"
	"github.com/ironpark/acp-go/internal/jsonrpc"
	schema "github.com/ironpark/acp-go/schema/v1"
)

// ClientSideConnection is the client's view of an ACP connection.
//
// It serves a [Client] to the peer agent and exposes every agent method for
// outgoing calls, so an editor drives a session entirely through this type.
//
// See protocol docs: [Client](https://agentclientprotocol.com/protocol/overview#client)
type ClientSideConnection struct {
	conn   *jsonrpc.Connection
	client Client
	turns  acpconn.Turns[SessionID, SessionUpdate, *PromptResponse]
}

var _ Agent = (*ClientSideConnection)(nil)

// NewClientSideConnection connects a client to an agent.
//
// newClient receives the connection being built, so the client can call the
// agent while handling one of its requests:
//
//	conn := acp1.NewClientSideConnection(func(c *acp1.ClientSideConnection) acp1.Client {
//		return &myClient{agent: c}
//	}, acp.NewStdioTransport(agentStdout, agentStdin))
//	go conn.Start(ctx)
//
// transport carries the messages to and from the agent; for an agent process
// it is [acp.NewStdioTransport] over its stdout and stdin, which [SpawnAgent]
// sets up.
//
// See protocol docs: [Communication Model](https://agentclientprotocol.com/protocol/overview#communication-model)
func NewClientSideConnection(newClient func(*ClientSideConnection) Client, transport acp.Transport, opts ...acp.Option) *ClientSideConnection {
	c := &ClientSideConnection{}
	c.client = newClient(c)
	c.conn = jsonrpc.New(c.handleRequest, c.handleNotification, transport, opts...)
	return c
}

// sessionUpdate copies each session update to the session's [Turn] in
// progress, then hands it to the client. The generated dispatch routes
// session/update here.
func (c *ClientSideConnection) sessionUpdate(ctx context.Context, n *SessionNotification) error {
	c.turns.Deliver(n.SessionID, n.Update)
	return c.client.SessionUpdate(ctx, n)
}

// UnimplementedClient provides the methods every [Client] needs for a client
// that only reads its turns: it drops session updates, which each [Turn] still
// collects, and answers permission requests with "method not found". Embed it
// and declare either method to override it:
//
//	type myClient struct{ acp1.UnimplementedClient }
//
// session/request_permission is part of every client in the protocol, so
// embed this only for agents that never ask.
type UnimplementedClient struct{}

// SessionUpdate ignores the update.
func (UnimplementedClient) SessionUpdate(context.Context, *SessionNotification) error { return nil }

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
