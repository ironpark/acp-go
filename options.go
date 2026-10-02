package acp

import (
	"context"
	"io"
	"time"

	"github.com/ironpark/acp-go/internal/jsonrpc"
)

// Transport is a bidirectional message transport. Implement it to carry ACP
// over something other than stdio; package acphttp provides Streamable HTTP
// and WebSocket.
type Transport = jsonrpc.Transport

// Middleware wraps incoming request and/or notification handling. Either field
// may be nil; the first middleware added is the outermost.
type Middleware = jsonrpc.Middleware

// RequestHandler handles one incoming request and returns the value to encode
// as its result.
type RequestHandler = jsonrpc.RequestHandler

// NotificationHandler handles one incoming notification.
type NotificationHandler = jsonrpc.NotificationHandler

// NewStdioTransport carries newline-delimited JSON over a reader/writer pair.
func NewStdioTransport(reader io.Reader, writer io.Writer) Transport {
	return jsonrpc.NewStdioTransport(reader, writer)
}

// Option configures a connection. The same option values configure the v2
// façade, so middleware or timeouts set up once serve either version.
type Option = jsonrpc.Option

// WithErrorHandler sets a callback for non-fatal errors: undecodable messages,
// write failures and errors returned by notification handlers.
func WithErrorHandler(h func(error)) Option { return jsonrpc.WithErrorHandler(h) }

// WithMiddleware adds middleware to the incoming handler chain.
func WithMiddleware(mw ...Middleware) Option { return jsonrpc.WithMiddleware(mw...) }

// WithWriteQueueSize sets the outgoing queue depth. Default: 100.
func WithWriteQueueSize(size int) Option { return jsonrpc.WithWriteQueueSize(size) }

// WithRequestTimeout bounds outgoing requests whose caller context carries no
// deadline of its own. Requests that wait for the user are exempt: a v1
// session/prompt, which lasts its whole turn, and session/request_permission
// and elicitation/create; bound those with their context, and mark other
// such requests with [WithoutRequestTimeout]. Default: none.
func WithRequestTimeout(d time.Duration) Option { return jsonrpc.WithRequestTimeout(d) }

// WithoutRequestTimeout returns a context whose requests the connection's
// [WithRequestTimeout] does not bound, for an extension request that waits
// for the user; ctx's own deadline still applies.
func WithoutRequestTimeout(ctx context.Context) context.Context { return jsonrpc.WithoutTimeout(ctx) }

// WithShutdownTimeout bounds how long Close waits for in-flight handlers.
// Default: wait indefinitely.
func WithShutdownTimeout(d time.Duration) Option { return jsonrpc.WithShutdownTimeout(d) }
