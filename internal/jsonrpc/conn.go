package jsonrpc

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Version is the JSON-RPC protocol version written on every message.
const Version = "2.0"

// CancelRequestMethod cancels a single in-flight request by id.
//
// It is handled by the connection itself in both directions and is never
// dispatched to a notification handler.
const CancelRequestMethod = "$/cancel_request"

// emptyObject is the result sent for a request whose handler returned no value.
var emptyObject = jsontext.Value(`{}`)

// errConnectionClosed fails the requests still pending when the connection stops.
var errConnectionClosed = errors.New("connection closed")

// RequestHandler handles an incoming request and returns the value to encode
// as the JSON-RPC result. A nil value is encoded as an empty object.
type RequestHandler func(ctx context.Context, method string, params jsontext.Value) (any, error)

// NotificationHandler handles an incoming notification. Returned errors are
// reported to the connection's error handler; notifications have no response.
type NotificationHandler func(ctx context.Context, method string, params jsontext.Value) error

// Middleware wraps incoming request and/or notification handling.
//
// Either field may be nil. Middleware is applied in the order it was added:
// the first one added is the outermost.
type Middleware struct {
	Request      func(next RequestHandler) RequestHandler
	Notification func(next NotificationHandler) NotificationHandler
}

// wireMessage is any JSON-RPC message: request, notification or response.
type wireMessage struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      jsontext.Value `json:"id,omitzero"`
	Method  string         `json:"method,omitzero"`
	Params  jsontext.Value `json:"params,omitzero"`
	Result  jsontext.Value `json:"result,omitzero"`
	Error   *wireError     `json:"error,omitzero"`
}

// Connection is a bidirectional JSON-RPC 2.0 connection over a [Transport].
type Connection struct {
	transport Transport

	request        RequestHandler
	notification   NotificationHandler
	requestContext func(ctx context.Context, method string, params jsontext.Value) context.Context

	pending  sync.Map // idKey -> *pendingResponse
	incoming sync.Map // idKey -> context.CancelCauseFunc

	nextRequestID atomic.Int64

	writeQueue chan jsontext.Value
	ctx        context.Context
	cancel     context.CancelFunc
	fail       context.CancelCauseFunc // cancels with the error that broke the connection

	wg        sync.WaitGroup // write loop
	handlerWg sync.WaitGroup // in-flight request handlers
	// lifecycle orders goroutine starts against Close: a WaitGroup must not
	// gain a goroutine while Close waits on it from zero.
	lifecycle sync.Mutex

	errorHandler    func(error)
	writeQueueSize  int
	requestTimeout  time.Duration
	shutdownTimeout time.Duration

	// Construction-only state, cleared by New.
	middlewares []Middleware
}

// Option configures a [Connection].
type Option func(*Connection)

// WithErrorHandler sets a callback for non-fatal errors: undecodable messages,
// write failures and notification handler errors.
func WithErrorHandler(h func(error)) Option {
	return func(c *Connection) { c.errorHandler = h }
}

// WithMiddleware adds middleware to the incoming handler chain.
func WithMiddleware(mw ...Middleware) Option {
	return func(c *Connection) { c.middlewares = append(c.middlewares, mw...) }
}

// WithRequestContext sets a function that derives each incoming request's
// context. It runs on the read loop in arrival order, before the request's
// handler starts, so it sees every request before any message read after it;
// it must not block.
func WithRequestContext(fn func(ctx context.Context, method string, params jsontext.Value) context.Context) Option {
	return func(c *Connection) { c.requestContext = fn }
}

// WithWriteQueueSize sets the outgoing queue depth. Default: 100.
func WithWriteQueueSize(size int) Option {
	return func(c *Connection) { c.writeQueueSize = size }
}

// WithRequestTimeout bounds how long [Connection.SendRequest] waits when the
// caller's context has no deadline of its own. Default: none.
func WithRequestTimeout(d time.Duration) Option {
	return func(c *Connection) { c.requestTimeout = d }
}

// WithShutdownTimeout bounds how long [Connection.Close] waits for in-flight
// handlers. Default: wait indefinitely.
func WithShutdownTimeout(d time.Duration) Option {
	return func(c *Connection) { c.shutdownTimeout = d }
}

type pendingResponse struct {
	result chan responseResult
}

type responseResult struct {
	data jsontext.Value
	err  error
}

// New creates a connection over transport. Either handler may be nil, in which
// case incoming requests are answered with "method not found" and incoming
// notifications are ignored.
func New(request RequestHandler, notification NotificationHandler, transport Transport, opts ...Option) *Connection {
	c := &Connection{
		transport:      transport,
		request:        request,
		notification:   notification,
		writeQueueSize: 100,
	}
	for _, opt := range opts {
		opt(c)
	}
	c.writeQueue = make(chan jsontext.Value, c.writeQueueSize)
	// The connection is usable before Start: outgoing messages queue up and the
	// write loop flushes them once it runs.
	c.ctx, c.fail = context.WithCancelCause(context.Background())
	c.cancel = func() { c.fail(nil) }

	for _, mw := range slices.Backward(c.middlewares) {
		if mw.Request != nil && c.request != nil {
			c.request = mw.Request(c.request)
		}
		if mw.Notification != nil && c.notification != nil {
			c.notification = mw.Notification(c.notification)
		}
	}
	c.middlewares = nil
	return c
}

// Start processes messages until the transport reports EOF, the transport
// fails, or ctx is cancelled.
func (c *Connection) Start(ctx context.Context) error {
	stop := context.AfterFunc(ctx, c.cancel)
	defer stop()

	if !c.goUnlessClosed(&c.wg, c.writeLoop) {
		c.failPending(errConnectionClosed)
		return context.Cause(c.ctx)
	}

	err := c.readLoop()
	if cause := context.Cause(c.ctx); cause != nil && errors.Is(err, context.Canceled) {
		err = cause // why, if a write failed
	}
	// The reader is done (typically EOF). Let in-flight handlers finish so
	// their responses reach the write loop, then let the write loop flush them
	// before Start returns: a caller that exits on return must not lose a reply.
	c.handlerWg.Wait()
	c.cancel()
	c.wg.Wait()
	c.failPending(errConnectionClosed)
	return err
}

// goUnlessClosed runs fn on wg, or reports false once the connection is
// closed.
func (c *Connection) goUnlessClosed(wg *sync.WaitGroup, fn func()) bool {
	c.lifecycle.Lock()
	defer c.lifecycle.Unlock()
	if c.ctx.Err() != nil {
		return false
	}
	wg.Go(fn)
	return true
}

// Close cancels the connection and waits for in-flight handlers.
func (c *Connection) Close() error {
	c.lifecycle.Lock()
	c.cancel()
	c.lifecycle.Unlock()
	waitAll := func() {
		c.handlerWg.Wait()
		c.wg.Wait()
	}
	if c.shutdownTimeout > 0 {
		done := make(chan struct{})
		go func() {
			waitAll()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(c.shutdownTimeout):
			return fmt.Errorf("shutdown timed out after %s", c.shutdownTimeout)
		}
	} else {
		waitAll()
	}
	c.failPending(errConnectionClosed)
	return nil
}

// Done is closed once the connection stops.
func (c *Connection) Done() <-chan struct{} { return c.ctx.Done() }

func (c *Connection) logError(err error) {
	if c.errorHandler != nil {
		c.errorHandler(err)
	}
}

// IDKey canonicalizes a request id so that 1 and 1.0 map to the same pending
// entry. Invalid ids fall back to their raw bytes.
func IDKey(id jsontext.Value) string {
	canonical := id.Clone()
	if err := canonical.Canonicalize(); err != nil {
		return string(id)
	}
	return string(canonical)
}

func (c *Connection) readLoop() error {
	for {
		select {
		case <-c.ctx.Done():
			return c.ctx.Err()
		default:
		}

		data, err := c.transport.ReadMessage(c.ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			if c.ctx.Err() != nil {
				return c.ctx.Err() // the read failed because the connection stopped
			}
			return err
		}
		if len(data) == 0 {
			continue
		}

		var msg wireMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			c.logError(fmt.Errorf("decode jsonrpc message: %w", err))
			continue
		}

		switch {
		case msg.Method != "" && len(msg.ID) > 0:
			// The request's context is registered here rather than in its
			// handler goroutine, so a cancellation read after the request
			// always finds it.
			ctx, cancel := c.acceptRequest(msg)
			if !c.goUnlessClosed(&c.handlerWg, func() { c.handleRequest(ctx, cancel, msg) }) {
				c.incoming.Delete(IDKey(msg.ID))
				cancel(nil)
				return c.ctx.Err()
			}
		case msg.Method != "":
			// Handled inline so notification ordering is preserved.
			c.handleNotification(msg)
		case len(msg.ID) > 0:
			c.handleResponse(msg)
		default:
			c.logError(fmt.Errorf("invalid jsonrpc message: %s", data))
			if len(msg.ID) > 0 {
				c.trySend(wireMessage{
					ID:    msg.ID.Clone(),
					Error: InvalidRequest("message has no method or result").toWire(),
				})
			}
		}
	}
}

func (c *Connection) writeLoop() {
	write := func(ctx context.Context, data jsontext.Value) error {
		err := c.transport.WriteMessage(ctx, data)
		if err != nil {
			err = fmt.Errorf("write jsonrpc message: %w", err)
			c.logError(err)
		}
		return err
	}
	for {
		select {
		case data := <-c.writeQueue:
			if err := write(c.ctx, data); err != nil {
				c.fail(err) // a broken transport ends the connection
				return
			}
		case <-c.ctx.Done():
			// Flush whatever is already queued before giving up.
			for {
				select {
				case data := <-c.writeQueue:
					if write(context.WithoutCancel(c.ctx), data) != nil {
						return
					}
				default:
					return
				}
			}
		}
	}
}

// send encodes and queues a message, dropping it if the connection is closing.
func (c *Connection) send(msg wireMessage) error {
	msg.JSONRPC = Version
	data, err := json.Marshal(&msg)
	if err != nil {
		return fmt.Errorf("encode jsonrpc message: %w", err)
	}
	select {
	case c.writeQueue <- data:
		return nil
	case <-c.ctx.Done():
		return context.Cause(c.ctx)
	}
}

func (c *Connection) trySend(msg wireMessage) {
	if err := c.send(msg); err != nil && !errors.Is(err, context.Canceled) {
		c.logError(err)
	}
}

// acceptRequest creates an incoming request's context and registers it for
// $/cancel_request.
func (c *Connection) acceptRequest(msg wireMessage) (context.Context, context.CancelCauseFunc) {
	ctx, cancel := context.WithCancelCause(c.ctx)
	c.incoming.Store(IDKey(msg.ID), cancel)
	if c.requestContext != nil {
		ctx = c.requestContext(ctx, msg.Method, msg.Params)
	}
	return ctx, cancel
}

func (c *Connection) handleRequest(ctx context.Context, cancel context.CancelCauseFunc, msg wireMessage) {
	defer func() {
		c.incoming.Delete(IDKey(msg.ID))
		cancel(nil)
	}()

	response := wireMessage{ID: msg.ID.Clone()}
	result, err := c.callRequest(ctx, msg.Method, msg.Params)
	switch {
	case err != nil:
		response.Error = toRequestError(ctx, err).toWire()
	case result == nil:
		response.Result = emptyObject
	default:
		data, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			response.Error = InternalError(marshalErr.Error()).toWire()
		} else {
			response.Result = data
		}
	}
	c.trySend(response)
}

// callRequest invokes the request handler, converting panics into errors so a
// faulty handler cannot take the connection down.
func (c *Connection) callRequest(ctx context.Context, method string, params jsontext.Value) (result any, err error) {
	if c.request == nil {
		return nil, MethodNotFound(method)
	}
	defer func() {
		if r := recover(); r != nil {
			result = nil
			err = InternalError(fmt.Sprintf("panic in handler for %s: %v", method, r))
		}
	}()
	return c.request(ctx, method, params)
}

// toRequestError maps a handler error onto a JSON-RPC error object. A handler
// that answered deliberately keeps its own error; one that merely gave up on a
// cancelled context reports -32800.
func toRequestError(ctx context.Context, err error) *RequestError {
	if reqErr, ok := errors.AsType[*RequestError](err); ok {
		return reqErr
	}
	if ctx.Err() != nil {
		if cause := context.Cause(ctx); cause != nil {
			if cancelled, ok := errors.AsType[*RequestError](cause); ok && cancelled.Code == CodeRequestCancelled {
				return cancelled
			}
		}
		return RequestCancelled("")
	}
	return InternalError(err.Error())
}

func (c *Connection) handleNotification(msg wireMessage) {
	if msg.Method == CancelRequestMethod {
		c.cancelIncoming(msg.Params)
		return
	}
	if c.notification == nil {
		return
	}
	ctx, cancel := context.WithCancel(c.ctx)
	defer cancel()

	err := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic in handler for %s: %v", msg.Method, r)
			}
		}()
		return c.notification(ctx, msg.Method, msg.Params)
	}()
	if err != nil {
		c.logError(fmt.Errorf("notification handler %s: %w", msg.Method, err))
	}
}

// cancelIncoming cancels the handler context of the request named by a
// $/cancel_request notification. Unknown ids are ignored, as the RFD allows.
func (c *Connection) cancelIncoming(params jsontext.Value) {
	var payload struct {
		RequestID jsontext.Value `json:"requestId"`
	}
	if err := json.Unmarshal(params, &payload); err != nil || len(payload.RequestID) == 0 {
		return
	}
	key := IDKey(payload.RequestID)
	if entry, ok := c.incoming.Load(key); ok {
		entry.(context.CancelCauseFunc)(RequestCancelled("").WithData(map[string]jsontext.Value{
			"requestId": payload.RequestID.Clone(),
		}))
	}
}

func (c *Connection) handleResponse(msg wireMessage) {
	entry, ok := c.pending.LoadAndDelete(IDKey(msg.ID))
	if !ok {
		// A response to a request we already gave up on (for example after
		// sending $/cancel_request). Dropping it is correct.
		return
	}
	pending := entry.(*pendingResponse)

	var result responseResult
	switch {
	case msg.Error != nil:
		result.err = msg.Error.toRequestError()
	case len(msg.Result) > 0:
		result.data = msg.Result.Clone()
	default:
		result.data = emptyObject
	}
	select {
	case pending.result <- result:
	default:
	}
}

// failPending releases every caller still waiting on a response.
func (c *Connection) failPending(err error) {
	c.pending.Range(func(key, value any) bool {
		if _, loaded := c.pending.LoadAndDelete(key); loaded {
			select {
			case value.(*pendingResponse).result <- responseResult{err: err}:
			default:
			}
		}
		return true
	})
}

// SendRequest sends a request and waits for its response.
//
// When ctx is cancelled the connection sends $/cancel_request for the
// outstanding id and returns ctx.Err().
func (c *Connection) SendRequest(ctx context.Context, method string, params any) (jsontext.Value, error) {
	wait, err := c.StartRequest(ctx, method, params)
	if err != nil {
		return nil, err
	}
	return wait()
}

// StartRequest sends a request and returns a function that waits for its
// response. The request is queued before StartRequest returns, so a message
// sent after it, such as a notification that refers to it, reaches the peer
// after the request. Call wait exactly once; it waits as [Connection.SendRequest]
// does.
func (c *Connection) StartRequest(ctx context.Context, method string, params any) (wait func() (jsontext.Value, error), err error) {
	msg := wireMessage{Method: method}
	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("encode params for %s: %w", method, err)
		}
		msg.Params = data
	}

	cancel := context.CancelFunc(func() {})
	if c.requestTimeout > 0 {
		if _, hasDeadline := ctx.Deadline(); !hasDeadline {
			ctx, cancel = context.WithTimeout(ctx, c.requestTimeout)
		}
	}
	msg.ID = jsontext.Value(strconv.FormatInt(c.nextRequestID.Add(1), 10))
	key := IDKey(msg.ID)
	pending := &pendingResponse{result: make(chan responseResult, 1)}
	c.pending.Store(key, pending)
	if err := c.send(msg); err != nil {
		c.pending.Delete(key)
		cancel()
		return nil, err
	}

	return func() (jsontext.Value, error) {
		defer cancel()
		defer c.pending.Delete(key)
		select {
		case result := <-pending.result:
			if result.err != nil {
				return nil, result.err
			}
			return result.data, nil
		case <-ctx.Done():
			c.sendCancelRequest(msg.ID)
			return nil, ctx.Err()
		case <-c.ctx.Done():
			return nil, context.Cause(c.ctx)
		}
	}, nil
}

// sendCancelRequest asks the peer to abandon an outstanding request. It is
// best-effort: the peer may ignore it or answer normally.
func (c *Connection) sendCancelRequest(id jsontext.Value) {
	msg := wireMessage{Method: CancelRequestMethod}
	data, err := json.Marshal(map[string]jsontext.Value{"requestId": id})
	if err != nil {
		return
	}
	msg.Params = data
	select {
	case <-c.ctx.Done():
	default:
		_ = c.send(msg)
	}
}

// SendNotification sends a notification, which has no response.
func (c *Connection) SendNotification(ctx context.Context, method string, params any) error {
	msg := wireMessage{Method: method}
	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("encode params for %s: %w", method, err)
		}
		msg.Params = data
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return c.send(msg)
}
