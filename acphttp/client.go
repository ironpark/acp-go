package acphttp

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"sync"
	"time"
)

// ClientTransport is the client side of Streamable HTTP. It needs no setup
// beyond the endpoint URL: the first message it sends must be initialize,
// whose reply carries the connection id; it then opens the connection stream,
// and a session's stream as soon as a message names the session.
//
// A stream that drops is reopened, backing off up to 5 seconds between tries,
// until the server answers that the connection or session is gone, refuses
// the stream (as with 401), or about 15 seconds of tries fail. Messages the
// server sent into a dropped stream may be lost: ACP v1 has no replay.
//
// Close deletes the connection on the server. A connection never closes its
// transport, so the caller does, after the connection stops.
type ClientTransport struct {
	url string
	clientConfig

	inbox       chan jsontext.Value
	done        chan struct{}
	closeOnce   sync.Once
	streamCtx   context.Context
	stopStreams context.CancelFunc
	streams     sync.WaitGroup
	streamEnded chan struct{} // closed once the connection stream stops
	readErr     error         // why it stopped: io.EOF, or a read error

	mu           sync.Mutex
	connectionID string
	sessions     map[string]bool   // sessions whose stream is open
	pendingLoads map[string]string // session/load request id -> session
}

// ClientOption configures [NewClientTransport] and
// [DialWebSocket].
type ClientOption func(*clientConfig)

type clientConfig struct {
	client       *http.Client
	header       http.Header
	jar          http.CookieJar
	pingInterval time.Duration
}

// newClientConfig applies opts over the defaults: a client with a cookie
// jar, since the protocol requires clients to keep the server's cookies for
// the connection.
func newClientConfig(opts []ClientOption) clientConfig {
	c := clientConfig{header: http.Header{}, pingInterval: keepaliveInterval}
	for _, opt := range opts {
		opt(&c)
	}
	switch {
	case c.client == nil:
		if c.jar == nil {
			c.jar, _ = cookiejar.New(nil)
		}
		c.client = &http.Client{Jar: c.jar}
	case c.jar != nil:
		client := *c.client // leave the caller's client as it is
		client.Jar = c.jar
		c.client = &client
	}
	return c
}

// WithHTTPClient sets the HTTP client. The protocol requires clients to keep
// cookies for the connection, so give it a Jar; the default client has one.
// Its Timeout must be zero, since the streams stay open.
func WithHTTPClient(client *http.Client) ClientOption {
	return func(c *clientConfig) { c.client = client }
}

// WithCookieJar keeps the server's cookies in jar instead of a jar of the
// transport's own. Reuse one jar across the transports of a reconnecting
// client: servers behind a load balancer rely on cookies to route it back to
// the backend holding its sessions. It overrides the jar of WithHTTPClient.
//
// Reconnecting in ACP v1 is a new connection: dial again with the same jar
// and headers, initialize, check the agent's loadSession capability, and load
// the saved session. Messages sent while disconnected are not replayed.
func WithCookieJar(jar http.CookieJar) ClientOption {
	return func(c *clientConfig) { c.jar = jar }
}

// WithPingInterval sets how often [DialWebSocket] pings the server; a server
// that does not answer within the next interval is disconnected, and
// ReadMessage reports it. The default is 15 seconds; zero or less disables
// pinging. Streamable HTTP ignores it. Servers set theirs with
// [WithWebSocketPing].
func WithPingInterval(interval time.Duration) ClientOption {
	return func(c *clientConfig) { c.pingInterval = interval }
}

// WithHeader adds a header to every request, such as Authorization.
func WithHeader(key, value string) ClientOption {
	return func(c *clientConfig) { c.header.Add(key, value) }
}

// NewClientTransport returns a transport to the ACP endpoint at url, such
// as "https://agent.example.com/acp".
func NewClientTransport(url string, opts ...ClientOption) *ClientTransport {
	ctx, cancel := context.WithCancel(context.Background())
	return &ClientTransport{
		url:          url,
		clientConfig: newClientConfig(opts),
		inbox:        make(chan jsontext.Value, streamBuffer),
		done:         make(chan struct{}),
		streamCtx:    ctx,
		stopStreams:  cancel,
		streamEnded:  make(chan struct{}),
		sessions:     map[string]bool{},
		pendingLoads: map[string]string{},
	}
}

// ReadMessage returns the next message from the agent. Once the connection
// stream ends, it returns the messages already received and then io.EOF, or
// the error that ended the stream, so the connection stops instead of waiting
// for replies that cannot arrive. A session stream ending is not an error.
func (t *ClientTransport) ReadMessage(ctx context.Context) (jsontext.Value, error) {
	select {
	case msg := <-t.inbox:
		return msg, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-t.done:
		return nil, io.EOF
	case <-t.streamEnded:
		select {
		case msg := <-t.inbox:
			return msg, nil
		case <-t.done:
			return nil, io.EOF // Close cancelled the stream; that is not a read error
		default:
			return nil, t.readErr
		}
	}
}

// WriteMessage posts one message to the agent.
func (t *ClientTransport) WriteMessage(ctx context.Context, data jsontext.Value) error {
	select {
	case <-t.done:
		return ErrTransportClosed
	default:
	}
	e, err := parseEnvelope(data)
	if err != nil {
		return err
	}
	// Close aborts a POST in flight: a connection flushing on its way out
	// writes without cancellation, and would otherwise wait on the server.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(t.streamCtx, cancel)()
	if e.Method == initializeMethod && e.isRequest() {
		t.mu.Lock()
		initialized := t.connectionID != ""
		t.mu.Unlock()
		if initialized {
			return errors.New("acp: initialize already sent on this transport")
		}
		return t.initialize(ctx, data)
	}

	t.mu.Lock()
	connectionID := t.connectionID
	session := e.paramsSession()
	loading := e.isRequest() && e.Method == loadSessionMethod && session != ""
	if loading {
		t.pendingLoads[e.idKey()] = session
	}
	t.mu.Unlock()
	if connectionID == "" {
		return fmt.Errorf("acp: %s sent before initialize", e.Method)
	}

	header := http.Header{ConnectionIDHeader: {connectionID}}
	if sessionScopedMethods[e.Method] && session != "" {
		header.Set(SessionIDHeader, session)
	}
	resp, err := t.post(ctx, data, header)
	if err == nil {
		err = t.accepted(resp)
	}
	if err != nil && loading {
		t.mu.Lock()
		delete(t.pendingLoads, e.idKey())
		t.mu.Unlock()
	}
	return err
}

// initialize posts the initialize request, whose reply comes back in the
// response body with the connection id, then opens the connection stream.
func (t *ClientTransport) initialize(ctx context.Context, data jsontext.Value) error {
	resp, err := t.post(ctx, data, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return statusError("initialize", resp)
	}
	connectionID := resp.Header.Get(ConnectionIDHeader)
	if connectionID == "" {
		return fmt.Errorf("acp: initialize response has no %s header", ConnectionIDHeader)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMessageSize))
	if err != nil {
		return err
	}
	t.mu.Lock()
	t.connectionID = connectionID
	t.mu.Unlock()
	t.receive(jsontext.Value(body))
	t.openStream("")
	return nil
}

func (t *ClientTransport) post(ctx context.Context, data jsontext.Value, header http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	t.setHeaders(req, header)
	req.Header.Set("Content-Type", "application/json")
	return t.client.Do(req)
}

// accepted checks a POST's status. 202 is the norm; a 200 body is a reply.
func (t *ClientTransport) accepted(resp *http.Response) error {
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusAccepted:
		return nil
	case http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxMessageSize))
		if err == nil && len(bytes.TrimSpace(body)) > 0 {
			t.receive(jsontext.Value(body))
		}
		return err
	}
	return statusError("POST", resp)
}

// StatusError is a request the server answered with an unexpected HTTP
// status, from either transport, so a caller can tell an authentication
// failure from a network one:
//
//	if status, ok := errors.AsType[*acphttp.StatusError](err); ok && status.StatusCode == http.StatusUnauthorized {
//		// refresh the credentials and connect again
//	}
type StatusError struct {
	// Op is the request that failed, such as "initialize" or "websocket upgrade".
	Op         string
	StatusCode int
	// Body is the start of the response body, if the server sent one.
	Body []byte
	// Err is the underlying error, if any.
	Err error
}

func (e *StatusError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("acp: %s: HTTP %d: %v", e.Op, e.StatusCode, e.Err)
	}
	return fmt.Sprintf("acp: %s: HTTP %d: %s", e.Op, e.StatusCode, e.Body)
}

func (e *StatusError) Unwrap() error { return e.Err }

func statusError(op string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodySize))
	return &StatusError{Op: op, StatusCode: resp.StatusCode, Body: bytes.TrimSpace(body)}
}

// Reopening a dropped stream waits streamBackoff, doubling per try up to
// maxStreamBackoff, and gives up after streamRetries tries in a row. A stream
// that stayed open for streamStable counts as healthy: tries start over.
const (
	streamBackoff    = 100 * time.Millisecond
	maxStreamBackoff = 5 * time.Second
	streamRetries    = 8
	streamStable     = 10 * time.Second
)

// retryableStatus reports a stream answer that reopening may change:
// timeouts, conflicts, rate limits and server errors.
func retryableStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooManyRequests:
		return true
	}
	return status >= 500
}

func (t *ClientTransport) setHeaders(req *http.Request, header http.Header) {
	for k, v := range t.header {
		req.Header[k] = v
	}
	for k, v := range header {
		req.Header[k] = v
	}
}

// openStream starts reading the connection stream (session "") or a
// session's stream, once per session.
func (t *ClientTransport) openStream(session string) {
	t.mu.Lock()
	if session != "" {
		if t.sessions[session] {
			t.mu.Unlock()
			return
		}
		t.sessions[session] = true
	}
	connectionID := t.connectionID
	t.mu.Unlock()

	t.streams.Go(func() {
		err := t.followStream(connectionID, session)
		if session != "" {
			t.mu.Lock()
			delete(t.sessions, session)
			t.mu.Unlock()
			return
		}
		// The connection stream is the only way replies arrive: losing it
		// ends the transport for reading.
		t.readErr = io.EOF
		if err != nil {
			t.readErr = err
		}
		close(t.streamEnded)
	})
}

// followStream reads a stream, reopening it when it drops, until the server
// no longer knows it (nil), the transport closes (nil), the server refuses it
// for good, as with 401, or reopening fails streamRetries times in a row (the
// last error, or nil if the stream kept ending cleanly).
func (t *ClientTransport) followStream(connectionID, session string) error {
	for failures := 0; ; {
		opened := time.Now()
		err := t.readStream(connectionID, session)
		status, _ := errors.AsType[*StatusError](err)
		switch {
		case t.streamCtx.Err() != nil:
			return nil
		case status != nil && status.StatusCode == http.StatusNotFound:
			return nil // the connection or session is gone
		case status != nil && !retryableStatus(status.StatusCode):
			return err
		}
		if time.Since(opened) >= streamStable {
			failures = 0
		}
		if failures++; failures > streamRetries {
			return err
		}
		select {
		case <-time.After(min(streamBackoff<<(failures-1), maxStreamBackoff)):
		case <-t.streamCtx.Done():
			return nil
		}
	}
}

func (t *ClientTransport) readStream(connectionID, session string) error {
	req, err := http.NewRequestWithContext(t.streamCtx, http.MethodGet, t.url, nil)
	if err != nil {
		return err
	}
	header := http.Header{"Accept": {"text/event-stream"}, ConnectionIDHeader: {connectionID}}
	if session != "" {
		header.Set(SessionIDHeader, session)
	}
	t.setHeaders(req, header)
	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("acp: open event stream: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return statusError("open event stream", resp)
	}
	if err := readSSE(resp.Body, t.receive); err != nil {
		return fmt.Errorf("acp: event stream: %w", err)
	}
	return nil
}

// receive queues a message from the agent, opening the stream of any
// session it names.
func (t *ClientTransport) receive(msg jsontext.Value) {
	if e, err := parseEnvelope(msg); err == nil {
		t.mu.Lock()
		var open []string
		if e.isResponse() {
			key := e.idKey()
			if loaded, ok := t.pendingLoads[key]; ok {
				delete(t.pendingLoads, key)
				if len(e.Result) > 0 {
					open = append(open, loaded)
				}
			}
		}
		session := e.paramsSession()
		if session == "" {
			session = e.resultSession()
		}
		// A load's replay arrives on the connection stream; its session
		// stream opens once the load succeeds, not for a load that fails.
		if session != "" && !t.loading(session) {
			open = append(open, session)
		}
		t.mu.Unlock()
		for _, s := range open {
			t.openStream(s)
		}
	}
	select {
	case t.inbox <- msg:
	case <-t.done:
	}
}

func (t *ClientTransport) loading(session string) bool {
	for _, s := range t.pendingLoads {
		if s == session {
			return true
		}
	}
	return false
}

// Close stops the streams and deletes the connection on the server.
func (t *ClientTransport) Close() error {
	t.closeOnce.Do(func() {
		close(t.done)
		t.stopStreams()
		t.mu.Lock()
		connectionID := t.connectionID
		t.mu.Unlock()
		if connectionID != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if req, err := http.NewRequestWithContext(ctx, http.MethodDelete, t.url, nil); err == nil {
				t.setHeaders(req, http.Header{ConnectionIDHeader: {connectionID}})
				if resp, err := t.client.Do(req); err == nil {
					resp.Body.Close()
				}
			}
		}
		t.streams.Wait()
	})
	return nil
}
