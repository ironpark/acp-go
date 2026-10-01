package acpmcp

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
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
	calls map[string]*link // requestId of each request in flight -> its session
}

func newDialer(request func(context.Context, message) (outcome, error)) *dialer {
	return &dialer{request: request, calls: map[string]*link{}}
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
	l := d.calls[m.requestID]
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

	mu      sync.Mutex
	queue   []jsonrpc.Message // read by the session, in order
	ready   chan struct{}     // signalled when queue gains a message
	pending map[string]context.CancelFunc
}

func newLink(d *dialer, serverID string) *link {
	ctx, cancel := context.WithCancel(context.Background())
	return &link{
		dialer: d, serverID: serverID, prefix: "mcp-request:" + rand.Text() + ":",
		ctx: ctx, cancel: cancel,
		ready: make(chan struct{}, 1), pending: map[string]context.CancelFunc{},
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
	l.mu.Lock()
	l.pending[m.requestID] = cancel
	l.mu.Unlock()
	l.dialer.mu.Lock()
	l.dialer.calls[m.requestID] = l
	l.dialer.mu.Unlock()
	// The answer arrives later as a response; Write must not wait for it.
	go func() {
		out, err := l.dialer.request(ctx, m)
		l.dialer.mu.Lock()
		delete(l.dialer.calls, m.requestID)
		l.dialer.mu.Unlock()
		l.mu.Lock()
		delete(l.pending, m.requestID)
		l.mu.Unlock()
		if ctx.Err() != nil {
			cancel()
			return // the session gave up on it, or closed
		}
		cancel()
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
	var params struct {
		RequestID jsontext.Value `json:"requestId"`
	}
	if err := jsonv2.Unmarshal(raw, &params); err != nil {
		return
	}
	var id any
	if err := jsonv2.Unmarshal(params.RequestID, &id); err != nil {
		return
	}
	if f, ok := id.(float64); ok { // the session's ids are integers
		id = int64(f)
	}
	l.mu.Lock()
	cancel := l.pending[l.prefix+fmt.Sprint(id)]
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (l *link) Close() error {
	l.cancel()
	return nil
}

func (l *link) SessionID() string { return "" }
