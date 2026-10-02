package acpmcp

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// dialer opens MCP sessions to servers a client provides: the
// version-neutral core of [DialerV1] and [DialerV2].
type dialer struct {
	request func(ctx context.Context, m message) (outcome, error)

	mu    sync.Mutex
	calls map[string]call // requestId of each request in flight
}

// call is a request in flight: the session that made it, and what cancels it.
type call struct {
	link   *link
	cancel context.CancelFunc
}

func newDialer(request func(context.Context, message) (outcome, error)) *dialer {
	return &dialer{request: request, calls: map[string]call{}}
}

func (d *dialer) dial(ctx context.Context, serverID string, client *mcp.Client, opts *mcp.ClientSessionOptions) (*mcp.ClientSession, error) {
	l := newLink(d, serverID)
	// Connect discovers the server with server/discover; MCP 2026-07-28
	// needs no handshake and keeps no session on the server.
	session, err := client.Connect(ctx, l, opts)
	if err != nil {
		l.Close()
		return nil, err
	}
	return session, nil
}

// notify hands a notification of a request in flight to the session that
// made it. A notification for no such request is late, and dropped.
func (d *dialer) notify(m message) {
	d.mu.Lock()
	l := d.calls[m.requestID].link
	d.mu.Unlock()
	if l == nil || l.serverID != m.serverID {
		return
	}
	params, err := fromParams(m.params)
	if err != nil {
		return
	}
	l.push(&jsonrpc.Request{Method: m.method, Params: params})
}

// link is the [mcp.Connection] of one MCP client session to a server the
// client provides. Each request the session writes becomes an mcp/message
// request with a requestId of its own, and its outcome comes back as the
// response; notifications/cancelled for it cancels the ACP request.
type link struct {
	dialer   *dialer
	serverID string
	prefix   string          // makes the requestIds of this session unique
	ctx      context.Context // ends when the session closes
	cancel   context.CancelFunc

	mu    sync.Mutex
	queue []jsonrpc.Message // read by the session, in order
	ready chan struct{}     // signalled when queue gains a message
}

func newLink(d *dialer, serverID string) *link {
	ctx, cancel := context.WithCancel(context.Background())
	return &link{
		dialer: d, serverID: serverID, prefix: "mcp-request:" + rand.Text() + ":",
		ctx: ctx, cancel: cancel,
		ready: make(chan struct{}, 1),
	}
}

// Connect makes link its own [mcp.Transport].
func (l *link) Connect(context.Context) (mcp.Connection, error) { return l, nil }

// push queues msg for the session without blocking, so a message from the
// client never stalls the ACP connection.
func (l *link) push(msg jsonrpc.Message) {
	l.mu.Lock()
	l.queue = append(l.queue, msg)
	l.mu.Unlock()
	select {
	case l.ready <- struct{}{}:
	default:
	}
}

func (l *link) Read(ctx context.Context) (jsonrpc.Message, error) {
	for {
		l.mu.Lock()
		if len(l.queue) > 0 {
			msg := l.queue[0]
			l.queue[0] = nil // let a large message go once read
			l.queue = l.queue[1:]
			l.mu.Unlock()
			return msg, nil
		}
		l.mu.Unlock()
		select {
		case <-l.ready:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-l.ctx.Done():
			return nil, mcp.ErrConnectionClosed
		}
	}
}

func (l *link) Write(_ context.Context, msg jsonrpc.Message) error {
	if l.ctx.Err() != nil {
		return mcp.ErrConnectionClosed
	}
	request, ok := msg.(*jsonrpc.Request)
	if !ok {
		return nil // the server sends no requests, so there is nothing to answer
	}
	if !request.IsCall() {
		if request.Method == "notifications/cancelled" {
			l.cancelCall(request.Params)
		}
		// The binding carries no other notifications to the server.
		return nil
	}
	params, err := toParams(request.Params)
	if err != nil {
		return err
	}
	m := message{serverID: l.serverID, requestID: l.prefix + fmt.Sprint(request.ID.Raw()), method: request.Method, params: params}
	ctx, cancel := context.WithCancel(l.ctx)
	d := l.dialer
	d.mu.Lock()
	d.calls[m.requestID] = call{link: l, cancel: cancel}
	d.mu.Unlock()
	// The answer arrives later as a response; Write must not wait for it.
	go func() {
		defer cancel()
		out, err := d.request(ctx, m)
		d.mu.Lock()
		delete(d.calls, m.requestID)
		d.mu.Unlock()
		if ctx.Err() != nil {
			return // the session gave up on it, or closed
		}
		response := &jsonrpc.Response{ID: request.ID}
		switch {
		case err != nil:
			response.Error = toWireError(err)
		case out.err != nil:
			response.Error = out.err
		default:
			response.Result = json.RawMessage(out.result)
		}
		l.push(response)
	}()
	return nil
}

// cancelCall cancels the ACP request carrying the MCP request params names.
func (l *link) cancelCall(raw json.RawMessage) {
	var params mcp.CancelledParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return
	}
	id, err := jsonrpc.MakeID(params.RequestID)
	if err != nil {
		return
	}
	d := l.dialer
	d.mu.Lock()
	c := d.calls[l.prefix+fmt.Sprint(id.Raw())]
	d.mu.Unlock()
	if c.cancel != nil {
		c.cancel()
	}
}

func (l *link) Close() error {
	l.cancel()
	return nil
}

func (l *link) SessionID() string { return "" }
