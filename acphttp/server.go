package acphttp

import (
	"context"
	"crypto/rand"
	"encoding/json/jsontext"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	acp "github.com/ironpark/acp-go"
)

const (
	// initializeTimeout bounds how long the initialize POST waits for the
	// agent's response, which it returns in its body.
	initializeTimeout = 30 * time.Second
	// keepaliveInterval is how often an idle SSE stream sends a comment, so
	// proxies do not time it out. It matches the TypeScript and Python SDKs.
	keepaliveInterval = 15 * time.Second
	// defaultIdleTimeout is how long a Streamable HTTP connection may go
	// without a connection stream before the server ends it; see
	// [WithIdleTimeout].
	defaultIdleTimeout = 5 * time.Minute
)

// Server serves agents over Streamable HTTP and WebSocket on one
// endpoint. Each initialize request, or each WebSocket, starts a connection
// and calls serve with its [acp.Transport]; serve runs the agent on it and returns
// when the connection ends:
//
//	server := acphttp.NewServer(func(ctx context.Context, t acp.Transport) error {
//		conn := acp1.NewAgentSideConnection(newAgent, t)
//		return conn.Start(ctx)
//	})
//	http.Handle("/acp", server)
//
// For an agent that speaks several protocol versions, serve can be a
// router.ProtocolRouter's Serve:
//
//	acphttp.NewServer(r.Serve)
//
// ctx carries the values of the request that started the connection, the
// initialize POST or the WebSocket upgrade, so what an authentication
// middleware stored there reaches the agent. It is cancelled when the client
// deletes the connection or the server closes, not when that request ends.
type Server struct {
	serve        func(ctx context.Context, t acp.Transport) error
	origins      []string
	idleTimeout  time.Duration
	pingInterval time.Duration
	onError      func(error)

	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	draining bool           // Shutdown started: no new connections
	running  sync.WaitGroup // serve calls in progress
	conns    map[string]*httpServerConn
	sockets  map[string]*WebSocketTransport
}

// ServerOption configures [NewServer].
type ServerOption func(*Server)

// WithWebSocketOrigins lets browser pages on other origins open WebSockets to
// the server. Without it only pages on the server's own host may, which keeps
// other sites from driving the agent with a visitor's cookies. Patterns match
// the Origin host with path.Match, or "scheme://host" if they contain "://".
func WithWebSocketOrigins(patterns ...string) ServerOption {
	return func(s *Server) { s.origins = append(s.origins, patterns...) }
}

// WithWebSocketPing sets how often the server pings each WebSocket client;
// a client that does not answer within the next interval is disconnected,
// ending its connection. The default is 15 seconds; zero or less disables
// pinging, leaving a vanished client to TCP. Clients set theirs with
// [WithPingInterval].
func WithWebSocketPing(interval time.Duration) ServerOption {
	return func(s *Server) { s.pingInterval = interval }
}

// WithIdleTimeout sets how long a Streamable HTTP connection may go without
// its connection stream open before the server ends it, as if the client had
// deleted it. A client that vanishes without a DELETE, because it crashed or
// lost the network, would otherwise keep its agent running forever. The wait
// starts when initialize returns the connection id, and POSTs restart it; it
// must leave the client time to open its stream. The default is five
// minutes; zero or less disables it.
//
// It is unrelated to http.Server.IdleTimeout, which closes idle TCP
// connections. WebSocket connections are checked with pings instead.
func WithIdleTimeout(d time.Duration) ServerOption {
	return func(s *Server) { s.idleTimeout = d }
}

// WithErrorHandler sets a function that receives the errors serve returns,
// other than those from its connection ending by cancellation. By default
// they are dropped.
func WithErrorHandler(h func(error)) ServerOption {
	return func(s *Server) { s.onError = h }
}

// NewServer returns a handler that runs serve once per connection.
func NewServer(serve func(ctx context.Context, t acp.Transport) error, opts ...ServerOption) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		serve: serve, ctx: ctx, cancel: cancel,
		idleTimeout: defaultIdleTimeout, pingInterval: keepaliveInterval,
		conns: map[string]*httpServerConn{}, sockets: map[string]*WebSocketTransport{},
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Shutdown stops the server accepting connections and waits for those in
// progress to end, which they do when their clients close them. If ctx ends
// first, Shutdown ends the remaining connections as Close does and returns
// ctx's error. Like Close, it does not stop the http.Server using it. Call
// this before that server's own Shutdown: the connections still need it to
// POST and stream until they end, and the http.Server would otherwise wait
// out its deadline on their event streams.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.draining = true
	s.mu.Unlock()
	done := make(chan struct{})
	go func() {
		s.running.Wait()
		close(done)
	}()
	select {
	case <-done:
		return s.Close()
	case <-ctx.Done():
		s.Close()
		return ctx.Err()
	}
}

// admit registers a new connection unless the server is closed or shutting
// down. Its context has r's values and ends with the server or when the
// cancel passed to register is called; release must be called once the
// connection's serve has returned.
func (s *Server) admit(r *http.Request, register func(cancel context.CancelFunc)) (ctx context.Context, release func(), ok bool) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	s.mu.Lock()
	if s.draining || s.ctx.Err() != nil {
		s.mu.Unlock()
		cancel()
		return nil, nil, false
	}
	register(cancel)
	s.running.Add(1)
	s.mu.Unlock()

	unlink := context.AfterFunc(s.ctx, cancel)
	return ctx, func() {
		unlink()
		cancel()
		s.running.Done()
	}, true
}

// run serves an admitted connection, reporting an error that its context
// ending did not cause.
func (s *Server) run(ctx context.Context, t acp.Transport) {
	if err := s.serve(ctx, t); err != nil && ctx.Err() == nil && s.onError != nil {
		s.onError(err)
	}
}

// Close ends every connection. It does not stop the http.Server using it.
func (s *Server) Close() error {
	s.cancel()
	s.mu.Lock()
	conns, sockets := s.conns, s.sockets
	s.conns, s.sockets = map[string]*httpServerConn{}, map[string]*WebSocketTransport{}
	s.mu.Unlock()
	for _, c := range conns {
		c.Close()
	}
	for _, t := range sockets {
		t.Close()
	}
	return nil
}

func (s *Server) untrack(id string) {
	s.mu.Lock()
	delete(s.sockets, id)
	s.mu.Unlock()
}

// ServeHTTP routes POST, GET and DELETE on the ACP endpoint, and GETs asking
// to upgrade to WebSocket.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.post(w, r)
	case http.MethodGet:
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			s.websocket(w, r)
			return
		}
		s.stream(w, r)
	case http.MethodDelete:
		s.delete(w, r)
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) connection(w http.ResponseWriter, r *http.Request) (*httpServerConn, bool) {
	id := r.Header.Get(ConnectionIDHeader)
	if id == "" {
		http.Error(w, "missing "+ConnectionIDHeader, http.StatusBadRequest)
		return nil, false
	}
	s.mu.Lock()
	c := s.conns[id]
	s.mu.Unlock()
	if c == nil {
		http.Error(w, "unknown connection", http.StatusNotFound)
		return nil, false
	}
	return c, true
}

func (s *Server) remove(id string) {
	s.mu.Lock()
	c := s.conns[id]
	delete(s.conns, id)
	s.mu.Unlock()
	if c != nil {
		c.Close()
	}
}

func (s *Server) post(w http.ResponseWriter, r *http.Request) {
	if mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mediaType != "application/json" {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxMessageSize))
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	msg := jsontext.Value(body)
	if msg.Kind() == '[' {
		http.Error(w, "batch requests are not supported", http.StatusNotImplemented)
		return
	}
	e, err := parseEnvelope(msg)
	if err != nil {
		http.Error(w, "invalid JSON-RPC message", http.StatusBadRequest)
		return
	}
	if e.Method == initializeMethod && e.isRequest() && r.Header.Get(ConnectionIDHeader) == "" {
		s.initialize(w, r, msg, e)
		return
	}

	c, ok := s.connection(w, r)
	if !ok {
		return
	}
	session := r.Header.Get(SessionIDHeader)
	if sessionScopedMethods[e.Method] && session == "" {
		http.Error(w, "missing "+SessionIDHeader, http.StatusBadRequest)
		return
	}
	if session != "" && !c.hasSession(session) {
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}
	c.touch()
	if err := c.deliver(r.Context(), msg, e); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) initialize(w http.ResponseWriter, r *http.Request, msg jsontext.Value, e envelope) {
	c := newHTTPServerConn(rand.Text(), e.idKey())
	ctx, release, ok := s.admit(r, func(cancel context.CancelFunc) {
		c.stop = cancel // a DELETE or Close ends serve
		s.conns[c.id] = c
	})
	if !ok {
		http.Error(w, "server closed", http.StatusServiceUnavailable)
		return
	}
	go func() {
		defer release()
		s.run(ctx, c)
		s.remove(c.id)
	}()

	fail := func(status int, text string) {
		s.remove(c.id)
		http.Error(w, text, status)
	}
	if err := c.deliver(r.Context(), msg, e); err != nil {
		fail(http.StatusServiceUnavailable, err.Error())
		return
	}
	timer := time.NewTimer(initializeTimeout)
	defer timer.Stop()
	select {
	case response := <-c.initResponse:
		// The client can open its stream once it has the id, so the idle
		// wait starts here; initializeTimeout covered the time before.
		c.watchIdle(s.idleTimeout, func() { s.remove(c.id) })
		w.Header().Set(ConnectionIDHeader, c.id)
		w.Header().Set("Content-Type", "application/json")
		w.Write(response)
	case <-timer.C:
		fail(http.StatusGatewayTimeout, "initialize timed out")
	case <-c.done:
		fail(http.StatusInternalServerError, "connection closed during initialize")
	case <-r.Context().Done():
		s.remove(c.id)
	}
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	if !acceptsEventStream(r.Header.Values("Accept")) {
		http.Error(w, "Accept must include text/event-stream", http.StatusNotAcceptable)
		return
	}
	c, ok := s.connection(w, r)
	if !ok {
		return
	}
	out := c.stream(r.Header.Get(SessionIDHeader))
	if out == nil {
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	// A client reopens a stream when it thinks it dropped, which may be
	// before the server notices: the newer GET takes over.
	replaced, ok := out.attach(r.Context())
	if !ok {
		http.Error(w, "stream closed", http.StatusNotFound)
		return
	}
	defer out.detach()
	if out == c.connStream {
		c.streamOpened()
		defer c.streamClosed()
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	rc := http.NewResponseController(w)
	keepalive := time.NewTicker(keepaliveInterval)
	defer keepalive.Stop()
	for {
		msg := out.takeUnsent()
		if msg == nil {
			select {
			case msg = <-out.messages:
			case <-keepalive.C:
				if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil || rc.Flush() != nil {
					return
				}
				continue
			case <-replaced:
				return
			case <-out.closed:
				return
			case <-r.Context().Done():
				return
			}
		}
		if err := writeSSE(w, msg); err != nil || rc.Flush() != nil {
			// The client's next GET sends it. A message lost after it was
			// written is not: ACP v1 has no replay.
			out.keepUnsent(msg)
			return
		}
	}
}

func acceptsEventStream(values []string) bool {
	for _, v := range values {
		for part := range strings.SplitSeq(v, ",") {
			if mediaType, _, _ := mime.ParseMediaType(strings.TrimSpace(part)); mediaType == "text/event-stream" {
				return true
			}
		}
	}
	return false
}

func (s *Server) delete(w http.ResponseWriter, r *http.Request) {
	c, ok := s.connection(w, r)
	if !ok {
		return
	}
	s.remove(c.id)
	w.WriteHeader(http.StatusAccepted)
}

// httpServerConn is the [acp.Transport] of one Streamable HTTP connection. POSTed
// messages are read by the agent; what the agent writes goes to the
// connection or a session stream.
type httpServerConn struct {
	id           string
	initID       string
	initResponse chan jsontext.Value
	incoming     chan jsontext.Value
	done         chan struct{}
	closeOnce    sync.Once
	stop         context.CancelFunc // ends serve; set when the server admits it

	mu            sync.Mutex
	idle          *time.Timer // ends the connection once it fires
	idleTimeout   time.Duration
	readers       int // GETs reading the connection stream
	connStream    *outbound
	sessions      map[string]*outbound
	pendingRoutes map[string]string // request id -> session its reply goes to
	pendingLoads  map[string]string // session/load request id -> session
	provisional   map[string]bool   // session streams opened for a load in flight
}

func newHTTPServerConn(id, initID string) *httpServerConn {
	return &httpServerConn{
		id:            id,
		initID:        initID,
		initResponse:  make(chan jsontext.Value, 1),
		incoming:      make(chan jsontext.Value),
		done:          make(chan struct{}),
		connStream:    newOutbound(),
		sessions:      map[string]*outbound{},
		pendingRoutes: map[string]string{},
		pendingLoads:  map[string]string{},
		provisional:   map[string]bool{},
	}
}

// watchIdle calls onIdle once the connection stream has been closed for d,
// counting from now. d <= 0 disables it.
func (c *httpServerConn) watchIdle(d time.Duration, onIdle func()) {
	if d <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.idleTimeout = d
	c.idle = time.AfterFunc(d, onIdle)
}

func (c *httpServerConn) streamOpened() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readers++
	if c.idle != nil {
		c.idle.Stop()
	}
}

func (c *httpServerConn) streamClosed() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readers--
	if c.idle != nil && c.readers == 0 {
		c.idle.Reset(c.idleTimeout)
	}
}

// touch restarts the idle wait of a connection without a stream.
func (c *httpServerConn) touch() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.idle != nil && c.readers == 0 {
		c.idle.Reset(c.idleTimeout)
	}
}

func (c *httpServerConn) hasSession(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessions[id] != nil
}

// stream returns the connection stream for "", or the session's stream.
func (c *httpServerConn) stream(session string) *outbound {
	if session == "" {
		return c.connStream
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessions[session]
}

// deliver hands a POSTed message to the agent, first noting where the reply
// to a session request must go.
func (c *httpServerConn) deliver(ctx context.Context, msg jsontext.Value, e envelope) error {
	if e.isRequest() {
		if session := e.paramsSession(); session != "" {
			c.mu.Lock()
			if e.Method == loadSessionMethod {
				c.pendingLoads[e.idKey()] = session
				if c.sessions[session] == nil {
					// Let the client open the stream while the replay runs.
					c.sessions[session] = newOutbound()
					c.provisional[session] = true
				}
			} else {
				c.pendingRoutes[e.idKey()] = session
			}
			c.mu.Unlock()
		}
	}
	select {
	case c.incoming <- msg:
		return nil
	case <-c.done:
		return ErrTransportClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *httpServerConn) ReadMessage(ctx context.Context) (jsontext.Value, error) {
	select {
	case msg := <-c.incoming:
		return msg, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		return nil, io.EOF
	}
}

func (c *httpServerConn) WriteMessage(ctx context.Context, data jsontext.Value) error {
	select {
	case <-c.done:
		return ErrTransportClosed
	default:
	}
	e, err := parseEnvelope(data)
	if err != nil {
		return err
	}
	if e.isResponse() && e.idKey() == c.initID {
		select {
		case c.initResponse <- data.Clone():
			return nil
		default:
			return errors.New("duplicate initialize response")
		}
	}
	return c.route(e).push(ctx, data.Clone(), c.done)
}

// route picks the stream for a message the agent sends.
func (c *httpServerConn) route(e envelope) *outbound {
	c.mu.Lock()
	defer c.mu.Unlock()
	session := e.paramsSession()
	if e.isResponse() {
		key := e.idKey()
		if loaded, ok := c.pendingLoads[key]; ok {
			delete(c.pendingLoads, key)
			if len(e.Result) > 0 {
				delete(c.provisional, loaded)
			} else if c.provisional[loaded] && !c.loading(loaded) {
				delete(c.provisional, loaded)
				c.sessions[loaded].close()
				delete(c.sessions, loaded)
			}
			return c.connStream
		}
		session = c.pendingRoutes[key]
		delete(c.pendingRoutes, key)
		if created := e.resultSession(); created != "" {
			// session/new, fork or resume: the client learns the id from this
			// reply on the connection stream, then opens the session stream.
			if c.sessions[created] == nil {
				c.sessions[created] = newOutbound()
			}
			delete(c.provisional, created)
			return c.connStream
		}
	}
	// A load's replay stays on the connection stream, ordered with its reply.
	if session == "" || c.loading(session) {
		return c.connStream
	}
	if out := c.sessions[session]; out != nil {
		return out
	}
	return c.connStream
}

// loading reports whether a session/load for session awaits its reply.
func (c *httpServerConn) loading(session string) bool {
	for _, s := range c.pendingLoads {
		if s == session {
			return true
		}
	}
	return false
}

func (c *httpServerConn) Close() error {
	c.closeOnce.Do(func() {
		close(c.done)
		if c.stop != nil {
			c.stop()
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.idle != nil {
			c.idle.Stop()
		}
		c.connStream.close()
		for _, out := range c.sessions {
			out.close()
		}
	})
	return nil
}

// outbound buffers the messages of one SSE stream until a GET reads them.
type outbound struct {
	messages  chan jsontext.Value
	closed    chan struct{}
	closeOnce sync.Once
	reader    chan struct{} // held by the GET reading the stream

	mu       sync.Mutex
	replaced chan struct{}  // closed when a newer GET takes over
	unsent   jsontext.Value // taken by a GET that failed to write it
}

func newOutbound() *outbound {
	return &outbound{
		messages: make(chan jsontext.Value, streamBuffer),
		closed:   make(chan struct{}),
		reader:   make(chan struct{}, 1),
	}
}

// push queues msg, waiting while the buffer is full: dropping a reply would
// leave the peer's request pending forever.
func (o *outbound) push(ctx context.Context, msg jsontext.Value, done <-chan struct{}) error {
	select {
	case o.messages <- msg:
		return nil
	case <-o.closed:
		return ErrTransportClosed
	case <-done:
		return ErrTransportClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// attach makes the calling GET the stream's reader once the current one, if
// any, has given way. The returned channel closes when a newer GET asks this
// one to give way in turn.
func (o *outbound) attach(ctx context.Context) (replaced <-chan struct{}, ok bool) {
	mine := make(chan struct{})
	o.mu.Lock()
	if o.replaced != nil {
		close(o.replaced)
	}
	o.replaced = mine
	o.mu.Unlock()
	select {
	case o.reader <- struct{}{}:
		return mine, true
	case <-o.closed:
	case <-ctx.Done():
	}
	return nil, false
}

func (o *outbound) detach() { <-o.reader }

// takeUnsent returns the message a previous GET failed to write, if any.
func (o *outbound) takeUnsent() jsontext.Value {
	o.mu.Lock()
	defer o.mu.Unlock()
	msg := o.unsent
	o.unsent = nil
	return msg
}

// keepUnsent holds msg for the next GET, which sends it before the rest.
func (o *outbound) keepUnsent(msg jsontext.Value) {
	o.mu.Lock()
	o.unsent = msg
	o.mu.Unlock()
}

func (o *outbound) close() { o.closeOnce.Do(func() { close(o.closed) }) }
