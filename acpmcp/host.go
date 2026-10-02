package acpmcp

import (
	"context"
	"crypto/rand"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"sync"

	acp "github.com/ironpark/acp-go"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// host serves the MCP servers a client provides: the version-neutral core of
// [HostV1] and [HostV2].
//
// Each mcp/message request is one MCP operation, served by a session of its
// own, as a stateless MCP server serves each HTTP request: everything the
// server writes on that session belongs to the operation, so its
// notifications go back under the request's serverId and requestId.
type host struct {
	notify func(ctx context.Context, m message) error

	mu      sync.Mutex
	servers map[string]*mcp.Server
	active  map[[2]string]bool // (serverId, requestId) of the operations running
}

func newHost(notify func(context.Context, message) error) *host {
	return &host{notify: notify, servers: map[string]*mcp.Server{}, active: map[[2]string]bool{}}
}

// add registers server and returns the id the agent addresses it by.
func (h *host) add(server *mcp.Server) string {
	id := "mcp-server:" + rand.Text()
	h.mu.Lock()
	h.servers[id] = server
	h.mu.Unlock()
	return id
}

// claim marks the operation m names as running, or reports why it cannot run.
func (h *host) claim(m message) (*mcp.Server, func(), error) {
	key := [2]string{m.serverID, m.requestID}
	h.mu.Lock()
	defer h.mu.Unlock()
	server := h.servers[m.serverID]
	switch {
	case server == nil:
		return nil, nil, &acp.RequestError{Code: ErrorCodeServerUnavailable, Message: "mcp server " + m.serverID + " is not available"}
	case h.active[key]:
		return nil, nil, acp.InvalidParams(fmt.Sprintf("mcp request %q is already active for server %s", m.requestID, m.serverID))
	}
	h.active[key] = true
	return server, func() {
		h.mu.Lock()
		delete(h.active, key)
		h.mu.Unlock()
	}, nil
}

// message runs the MCP request m and returns its outcome. When ctx ends
// first, the server is told to cancel, and message still waits for it to
// answer: until then the request id stays in use.
func (h *host) message(ctx context.Context, m message) (outcome, error) {
	server, release, err := h.claim(m)
	if err != nil {
		return outcome{}, err
	}
	defer release()
	params, err := fromParams(m.params)
	if err != nil {
		return outcome{}, acp.InvalidParams(err.Error())
	}
	id, err := jsonrpc.MakeID(m.requestID)
	if err != nil {
		return outcome{}, acp.InvalidParams(err.Error())
	}

	op := newOperation(h, m, id)
	session, err := server.Connect(context.WithoutCancel(ctx), op, nil)
	if err != nil {
		return outcome{}, &acp.RequestError{Code: ErrorCodeBackendFailed, Message: err.Error()}
	}
	defer session.Close()
	op.incoming <- &jsonrpc.Request{ID: id, Method: m.method, Params: params}

	var response *jsonrpc.Response
	select {
	case response = <-op.response:
	case <-op.done:
		return outcome{}, &acp.RequestError{Code: ErrorCodeBackendFailed, Message: "mcp server " + m.serverID + " ended the request without an outcome"}
	case <-ctx.Done():
		op.stop()
		cancelled, _ := jsonv2.Marshal(map[string]string{"requestId": m.requestID})
		op.deliver(&jsonrpc.Request{Method: "notifications/cancelled", Params: cancelled})
		select {
		case <-op.response:
		case <-op.done:
		}
		return outcome{}, ctx.Err()
	}
	if response.Error != nil {
		if wire, ok := errors.AsType[*jsonrpc.Error](response.Error); ok {
			return outcome{err: wire}, nil
		}
		return outcome{}, &acp.RequestError{Code: ErrorCodeBackendFailed, Message: response.Error.Error()}
	}
	result := jsontext.Value(response.Result)
	if len(result) == 0 {
		result = jsontext.Value("null")
	}
	return outcome{result: result}, nil
}

// operation is the [mcp.Connection] of one MCP request served by a host: it
// reads the request, then waits; what the server writes is the request's
// notifications and, last, its response. A write it cannot carry closes it,
// so the request ends with an error rather than waiting for an answer the
// broken session will not send.
type operation struct {
	host     *host
	message  message
	id       jsonrpc.ID
	incoming chan jsonrpc.Message   // the request, then at most a cancellation
	response chan *jsonrpc.Response // the answer, once
	done     chan struct{}          // closed by Close
	once     sync.Once

	mu       sync.Mutex
	answered bool // the response was sent
	stopped  bool // answered, or cancelled: no more notifications
}

func newOperation(h *host, m message, id jsonrpc.ID) *operation {
	return &operation{
		host: h, message: m, id: id,
		incoming: make(chan jsonrpc.Message, 2),
		response: make(chan *jsonrpc.Response, 1),
		done:     make(chan struct{}),
	}
}

// Connect makes operation its own [mcp.Transport].
func (o *operation) Connect(context.Context) (mcp.Connection, error) { return o, nil }

// SupportsProtocolVersion limits server/discover to the revision the binding
// carries.
func (o *operation) SupportsProtocolVersion(version string) bool { return version == protocolVersion }

func (o *operation) Read(ctx context.Context) (jsonrpc.Message, error) {
	select {
	case msg := <-o.incoming:
		return msg, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-o.done:
		return nil, mcp.ErrConnectionClosed
	}
}

func (o *operation) Write(ctx context.Context, msg jsonrpc.Message) error {
	switch msg := msg.(type) {
	case *jsonrpc.Response:
		if msg.ID != o.id {
			return o.fail(fmt.Errorf("acpmcp: response to unknown request %v", msg.ID.Raw()))
		}
		o.mu.Lock()
		defer o.mu.Unlock()
		if !o.answered {
			o.answered, o.stopped = true, true
			o.response <- msg
		}
		return nil
	case *jsonrpc.Request:
		if msg.IsCall() {
			if msg.Method == "ping" {
				// A keepalive ping: the binding has no one to ask, so the
				// operation answers it itself.
				go o.deliver(&jsonrpc.Response{ID: msg.ID, Result: []byte("{}")})
				return nil
			}
			return o.fail(errors.New("acpmcp: an MCP server cannot send requests over ACP"))
		}
		params, err := toParams(msg.Params)
		if err != nil {
			return nil // not an MCP notification; there is no one to report it to
		}
		m := o.message
		m.method, m.params = msg.Method, params
		// Held across the send, so no notification follows the answer.
		o.mu.Lock()
		defer o.mu.Unlock()
		if o.stopped {
			return nil
		}
		if err := o.host.notify(ctx, m); err != nil {
			return o.fail(err)
		}
		return nil
	}
	return o.fail(fmt.Errorf("acpmcp: unexpected message %T", msg))
}

// fail closes the operation, ending its request, and returns err.
func (o *operation) fail(err error) error {
	o.Close()
	return err
}

// stop drops the notifications the server sends from now on, once the
// request is cancelled.
func (o *operation) stop() {
	o.mu.Lock()
	o.stopped = true
	o.mu.Unlock()
}

// deliver hands the server msg to read, unless the operation has closed.
func (o *operation) deliver(msg jsonrpc.Message) {
	select {
	case o.incoming <- msg:
	case <-o.done:
	}
}

func (o *operation) Close() error {
	o.once.Do(func() { close(o.done) })
	return nil
}

func (o *operation) SessionID() string { return "" }
