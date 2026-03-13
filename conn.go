package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// maxMessageSize is the maximum size of a single JSON-RPC message (50MB).
	maxMessageSize = 50 * 1024 * 1024
	// initialBufSize is the initial buffer size for reading messages (64KB).
	initialBufSize = 64 * 1024
	// jsonrpcVersion is the JSON-RPC protocol version.
	jsonrpcVersion = "2.0"
)

// emptyObjectJSON is a reusable empty JSON object for void-success responses.
var emptyObjectJSON = json.RawMessage("{}")

// Transport represents a bidirectional message transport for JSON-RPC communication.
//
// Implementations handle the encoding/framing details for different transport layers
// (stdio, HTTP+SSE, etc.). The Connection uses this interface to read and write messages.
type Transport interface {
	// ReadMessage reads the next JSON-RPC message from the transport.
	// Returns io.EOF when the transport is closed.
	// May return nil data for empty/skip messages (e.g., empty lines in stdio).
	ReadMessage() (json.RawMessage, error)

	// WriteMessage sends a JSON-RPC message over the transport.
	WriteMessage(data json.RawMessage) error

	// Close closes the transport and releases resources.
	Close() error
}

// StdioTransport implements Transport over io.Reader/io.Writer using newline-delimited JSON.
//
// This is the default transport used by ACP connections. Messages are separated by newlines,
// with a 50MB buffer to handle large payloads (e.g., base64-encoded images).
type StdioTransport struct {
	reader  io.Reader
	scanner *bufio.Scanner
	writer  io.Writer
	mu      sync.Mutex
}

// NewStdioTransport creates a new stdio transport over the given reader and writer.
func NewStdioTransport(reader io.Reader, writer io.Writer) *StdioTransport {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, initialBufSize), maxMessageSize)
	return &StdioTransport{
		reader:  reader,
		scanner: scanner,
		writer:  writer,
	}
}

func (t *StdioTransport) ReadMessage() (json.RawMessage, error) {
	if !t.scanner.Scan() {
		if err := t.scanner.Err(); err != nil {
			return nil, err
		}
		return nil, io.EOF
	}
	data := t.scanner.Bytes()
	if len(data) == 0 {
		return nil, nil
	}
	cp := make(json.RawMessage, len(data))
	copy(cp, data)
	return cp, nil
}

var newline = []byte{'\n'}

func (t *StdioTransport) WriteMessage(data json.RawMessage) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, err := t.writer.Write(data); err != nil {
		return err
	}
	_, err := t.writer.Write(newline)
	return err
}

func (t *StdioTransport) Close() error {
	var errs [2]error
	if closer, ok := t.reader.(io.Closer); ok {
		errs[0] = closer.Close()
	}
	if closer, ok := t.writer.(io.Closer); ok {
		errs[1] = closer.Close()
	}
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// Middleware wraps a MethodHandler, allowing pre/post processing of JSON-RPC method calls.
//
// Middleware is applied in order: the first middleware added is the outermost (runs first).
// Call next to continue the chain; skip it to short-circuit the request.
//
// Example:
//
//	func loggingMiddleware(next acp.MethodHandler) acp.MethodHandler {
//	    return func(ctx context.Context, method string, params json.RawMessage) (any, error) {
//	        log.Printf("method: %s", method)
//	        return next(ctx, method, params)
//	    }
//	}
type Middleware func(next MethodHandler) MethodHandler

// Connection represents a bidirectional JSON-RPC connection
type Connection struct {
	transport        Transport
	pendingResponses sync.Map // map[int64]*pendingResponse
	nextRequestID    int64
	composedHandler  MethodHandler // handler with middleware applied
	writeQueue       chan jsonRpcMessage
	ctx              context.Context
	cancel           context.CancelFunc
	wg               sync.WaitGroup // tracks in-flight handlers
	errorHandler     func(error)
	middlewares      []Middleware
	handler          MethodHandler // raw handler, used only during construction
	writeQueueSize   int
	requestTimeout   time.Duration
	shutdownTimeout  time.Duration
}

// ConnectionOption configures a Connection.
type ConnectionOption func(*Connection)

// WithErrorHandler sets a callback for non-fatal errors (parse failures, write errors, etc.).
func WithErrorHandler(h func(error)) ConnectionOption {
	return func(c *Connection) { c.errorHandler = h }
}

// WithMiddleware adds middleware to the connection's handler chain.
// Middleware is applied in order: first middleware is outermost (runs first).
func WithMiddleware(mw ...Middleware) ConnectionOption {
	return func(c *Connection) {
		c.middlewares = append(c.middlewares, mw...)
	}
}

// WithTransport sets a custom transport for the connection.
// When set, the reader/writer passed to NewConnection are ignored.
func WithTransport(t Transport) ConnectionOption {
	return func(c *Connection) { c.transport = t }
}

// WithWriteQueueSize sets the write queue buffer size. Default: 100.
func WithWriteQueueSize(size int) ConnectionOption {
	return func(c *Connection) { c.writeQueueSize = size }
}

// WithRequestTimeout sets a default timeout for pending responses.
// If no response is received within this duration and the caller's context
// has no deadline, the request returns context.DeadlineExceeded.
// Default: 0 (no timeout, rely on caller context).
func WithRequestTimeout(d time.Duration) ConnectionOption {
	return func(c *Connection) { c.requestTimeout = d }
}

// WithShutdownTimeout sets the maximum time to wait for in-flight requests
// during Close(). After this timeout, Close returns with an error.
// Default: 0 (wait indefinitely).
func WithShutdownTimeout(d time.Duration) ConnectionOption {
	return func(c *Connection) { c.shutdownTimeout = d }
}

// MethodHandler handles incoming JSON-RPC method calls.
// The context is derived from the connection's context.
type MethodHandler func(ctx context.Context, method string, params json.RawMessage) (any, error)

// pendingResponse represents a response waiting for completion
type pendingResponse struct {
	result chan responseResult
}

// responseResult contains the result or error from a JSON-RPC call
type responseResult struct {
	data  json.RawMessage
	error error
}

// jsonRpcMessage represents a JSON-RPC message
type jsonRpcMessage struct {
	Jsonrpc string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRpcError   `json:"error,omitempty"`
}

// jsonRpcError represents a JSON-RPC error
type jsonRpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// marshalParams marshals params to JSON, returning nil if params is nil.
func marshalParams(params any) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	data, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal params: %w", err)
	}
	return data, nil
}

// NewConnection creates a new bidirectional JSON-RPC connection.
//
// The reader and writer provide the default stdio transport.
// Use WithTransport to override with a custom transport (e.g., HTTP+SSE).
func NewConnection(handler MethodHandler, reader io.Reader, writer io.Writer, opts ...ConnectionOption) *Connection {
	conn := &Connection{
		handler:        handler,
		writeQueueSize: 100,
	}
	for _, opt := range opts {
		opt(conn)
	}
	if conn.transport == nil {
		conn.transport = NewStdioTransport(reader, writer)
	}
	conn.writeQueue = make(chan jsonRpcMessage, conn.writeQueueSize)

	// Compose middleware chain: first middleware is outermost
	conn.composedHandler = conn.handler
	for i := len(conn.middlewares) - 1; i >= 0; i-- {
		conn.composedHandler = conn.middlewares[i](conn.composedHandler)
	}
	// Clear construction-only fields
	conn.handler = nil
	conn.middlewares = nil

	return conn
}

// Start begins processing JSON-RPC messages.
// The provided context governs the connection lifecycle.
func (c *Connection) Start(ctx context.Context) error {
	c.ctx, c.cancel = context.WithCancel(ctx)

	// Start writer goroutine
	c.wg.Go(func() {
		c.writeLoop()
	})

	// Start reader loop (blocks until done)
	err := c.readLoop()
	// Reader exited — shut down the connection
	c.cancel()
	return err
}

// Close closes the connection gracefully.
// It cancels the connection context and waits for in-flight handlers to complete.
// If a shutdown timeout is configured, Close returns an error if handlers don't finish in time.
func (c *Connection) Close() error {
	c.cancel()
	if c.shutdownTimeout > 0 {
		done := make(chan struct{})
		go func() {
			c.wg.Wait()
			close(done)
		}()
		select {
		case <-done:
			return nil
		case <-time.After(c.shutdownTimeout):
			return fmt.Errorf("shutdown timed out after %s", c.shutdownTimeout)
		}
	}
	c.wg.Wait()
	return nil
}

// Done returns a channel that is closed when the connection is done.
func (c *Connection) Done() <-chan struct{} {
	return c.ctx.Done()
}

// logError logs an error using the errorHandler if set.
func (c *Connection) logError(err error) {
	if c.errorHandler != nil {
		c.errorHandler(err)
	}
}

// handlerContext returns a context that is cancelled when the connection closes.
func (c *Connection) handlerContext() (context.Context, context.CancelFunc) {
	handlerCtx, handlerCancel := context.WithCancel(c.ctx)
	return handlerCtx, handlerCancel
}

// readLoop reads and processes incoming messages using the transport.
func (c *Connection) readLoop() error {
	for {
		select {
		case <-c.ctx.Done():
			return c.ctx.Err()
		default:
		}

		data, err := c.transport.ReadMessage()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if data == nil {
			continue
		}

		var msg jsonRpcMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			c.logError(fmt.Errorf("failed to parse JSON-RPC message: %w", err))
			continue
		}

		if msg.ID != nil && msg.Method != "" {
			// It's a request — dispatch handler in goroutine
			c.wg.Go(func() {
				c.handleRequest(msg)
			})
		} else if msg.Method != "" {
			// It's a notification — dispatch handler in goroutine
			c.wg.Go(func() {
				c.handleNotification(msg)
			})
		} else if msg.ID != nil {
			// It's a response — handle inline to avoid ordering issues
			c.handleResponse(msg)
		}
	}
}

// writeLoop writes outgoing messages using the transport.
func (c *Connection) writeLoop() {
	for {
		select {
		case msg := <-c.writeQueue:
			data, err := json.Marshal(msg)
			if err != nil {
				c.logError(fmt.Errorf("failed to marshal JSON-RPC message: %w", err))
				continue
			}

			if err := c.transport.WriteMessage(data); err != nil {
				c.logError(fmt.Errorf("failed to write JSON-RPC message: %w", err))
				c.cancel() // close connection on write failure
				return
			}

		case <-c.ctx.Done():
			return
		}
	}
}

// trySend attempts to send a message on the write queue, respecting context cancellation.
func (c *Connection) trySend(msg jsonRpcMessage) {
	select {
	case c.writeQueue <- msg:
	case <-c.ctx.Done():
	}
}

// handleRequest processes incoming requests using the composed handler (with middleware).
func (c *Connection) handleRequest(msg jsonRpcMessage) {
	handlerCtx, handlerCancel := c.handlerContext()
	defer handlerCancel()

	response := jsonRpcMessage{
		Jsonrpc: jsonrpcVersion,
		ID:      msg.ID,
	}

	if c.composedHandler != nil {
		result, err := c.composedHandler(handlerCtx, msg.Method, msg.Params)
		if err != nil {
			if reqErr, ok := err.(*RequestError); ok {
				response.Error = &jsonRpcError{
					Code:    int(reqErr.Code),
					Message: reqErr.Msg,
				}
				if reqErr.Details != nil {
					if data, marshalErr := json.Marshal(reqErr.Details); marshalErr == nil {
						response.Error.Data = data
					}
				}
			} else {
				response.Error = &jsonRpcError{
					Code:    int(ErrorCodeInternalError),
					Message: err.Error(),
				}
			}
		} else if result != nil {
			if data, marshalErr := json.Marshal(result); marshalErr == nil {
				response.Result = data
			} else {
				response.Error = &jsonRpcError{
					Code:    int(ErrorCodeInternalError),
					Message: "Failed to marshal result",
				}
			}
		} else {
			// Void success — JSON-RPC requires a result field
			response.Result = emptyObjectJSON
		}
	} else {
		response.Error = &jsonRpcError{
			Code:    int(ErrorCodeMethodNotFound),
			Message: "Method not found",
		}
	}

	c.trySend(response)
}

// handleNotification processes incoming notifications using the composed handler (with middleware).
func (c *Connection) handleNotification(msg jsonRpcMessage) {
	if c.composedHandler != nil {
		handlerCtx, handlerCancel := c.handlerContext()
		defer handlerCancel()

		if _, err := c.composedHandler(handlerCtx, msg.Method, msg.Params); err != nil {
			c.logError(fmt.Errorf("notification handler error for %s: %w", msg.Method, err))
		}
	}
}

// handleResponse processes incoming responses
func (c *Connection) handleResponse(msg jsonRpcMessage) {
	if msg.ID == nil {
		return
	}

	if pending, ok := c.pendingResponses.LoadAndDelete(*msg.ID); ok {
		p := pending.(*pendingResponse)

		var result responseResult
		if msg.Error != nil {
			result.error = &RequestError{
				Code: ErrorCode(msg.Error.Code),
				Msg:  msg.Error.Message,
			}
		} else {
			result.data = msg.Result
		}

		select {
		case p.result <- result:
		default:
		}
	}
}

// sendRequest is a generic helper that sends a JSON-RPC request and unmarshals the response.
func sendRequest[T any](ctx context.Context, conn *Connection, method string, params any) (*T, error) {
	data, err := conn.SendRequest(ctx, method, params)
	if err != nil {
		return nil, err
	}
	var response T
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

// SendRequest sends a JSON-RPC request and waits for the response.
func (c *Connection) SendRequest(ctx context.Context, method string, params any) (json.RawMessage, error) {
	// Apply request timeout if configured and caller has no deadline
	if c.requestTimeout > 0 {
		if _, hasDeadline := ctx.Deadline(); !hasDeadline {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, c.requestTimeout)
			defer cancel()
		}
	}

	// Generate unique request ID
	requestID := atomic.AddInt64(&c.nextRequestID, 1)

	// Create response channel
	pending := &pendingResponse{
		result: make(chan responseResult, 1),
	}

	// Store pending response
	c.pendingResponses.Store(requestID, pending)

	// Cleanup on exit — just delete from map, don't close channel (avoids race)
	defer c.pendingResponses.Delete(requestID)

	// Prepare the request message
	msg := jsonRpcMessage{
		Jsonrpc: jsonrpcVersion,
		ID:      &requestID,
		Method:  method,
	}

	paramData, err := marshalParams(params)
	if err != nil {
		return nil, err
	}
	msg.Params = paramData

	// Send the request
	select {
	case c.writeQueue <- msg:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.ctx.Done():
		return nil, c.ctx.Err()
	}

	// Wait for response — no hardcoded timeout, rely on ctx
	select {
	case result := <-pending.result:
		if result.error != nil {
			return nil, result.error
		}
		return result.data, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.ctx.Done():
		return nil, c.ctx.Err()
	}
}

// SendNotification sends a JSON-RPC notification (no response expected)
func (c *Connection) SendNotification(ctx context.Context, method string, params any) error {
	msg := jsonRpcMessage{
		Jsonrpc: jsonrpcVersion,
		Method:  method,
	}

	paramData, err := marshalParams(params)
	if err != nil {
		return err
	}
	msg.Params = paramData

	select {
	case c.writeQueue <- msg:
		return nil
	case <-c.ctx.Done():
		return c.ctx.Err()
	}
}
