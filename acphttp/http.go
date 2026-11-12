package acphttp

import (
	"bufio"
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"

	"github.com/ironpark/acp-go/internal/jsonrpc"
)

// Streamable HTTP carries ACP over one endpoint, following the draft RFD
// "Streamable HTTP & WebSocket Transport" that the TypeScript and Python SDKs
// implement:
//
//   - POST sends a client message. The initialize request is answered in the
//     response body with an Acp-Connection-Id header; every other POST gets
//     202 Accepted and its reply arrives on an SSE stream.
//   - GET with Acp-Connection-Id opens the connection-scoped SSE stream; adding
//     Acp-Session-Id opens that session's stream. Session updates, requests to
//     the client and replies to session-scoped requests use the session
//     stream, everything else the connection stream.
//   - DELETE with Acp-Connection-Id ends the connection.
//   - GET with "Upgrade: websocket" instead opens a WebSocket that carries
//     the whole connection; see [WebSocketTransport].
//
// The RFD requires HTTP/2 so the streams and POSTs share one TCP connection;
// the Go implementation also works over HTTP/1.1, where the client opens one
// TCP connection per stream.
const (
	// ConnectionIDHeader identifies an initialized connection on every request
	// after initialize.
	ConnectionIDHeader = "Acp-Connection-Id"
	// SessionIDHeader names the session of a session-scoped POST or stream.
	SessionIDHeader = "Acp-Session-Id"
)

// ErrTransportClosed is returned when a message is sent on a closed transport.
var ErrTransportClosed = errors.New("transport closed")

const (
	// maxMessageSize is the largest single message accepted (50MB).
	maxMessageSize = 50 * 1024 * 1024
	// maxErrorBodySize bounds how much of an HTTP error body is quoted back.
	maxErrorBodySize = 1024
	// streamBuffer is how many messages a stream holds before its reader
	// attaches or while it lags; writers then wait rather than drop a reply.
	streamBuffer = 1024
)

// sessionScopedMethods are the requests on an existing session: their POSTs
// carry Acp-Session-Id and their replies use the session stream. Requests that
// create or attach a session (see reattachMethods) reply on the connection
// stream, since the client need not have the session stream open yet. The wire names are
// the same in every protocol version.
var sessionScopedMethods = map[string]bool{
	"session/set_mode":          true,
	"session/set_config_option": true,
	"session/prompt":            true,
	"session/cancel":            true,
	"session/close":             true,
}

const initializeMethod = "initialize"

// reattachMethods bring a session the client already knows by id onto this
// connection. The client may open the session's stream before it sends one,
// and the session is established here once the agent answers it with a
// result; until then its replay stays on the connection stream, ordered with
// the reply.
var reattachMethods = map[string]bool{
	"session/load":   true,
	"session/resume": true,
}

// envelope is what routing reads from a JSON-RPC message.
type envelope struct {
	ID     jsontext.Value `json:"id,omitzero"`
	Method string         `json:"method,omitzero"`
	Params jsontext.Value `json:"params,omitzero"`
	Result jsontext.Value `json:"result,omitzero"`
}

func parseEnvelope(data jsontext.Value) (envelope, error) {
	var e envelope
	err := json.Unmarshal(data, &e)
	return e, err
}

func (e envelope) isRequest() bool  { return e.Method != "" && len(e.ID) > 0 }
func (e envelope) isResponse() bool { return e.Method == "" && len(e.ID) > 0 }
func (e envelope) idKey() string    { return jsonrpc.IDKey(e.ID) }

// paramsSession is the sessionId in a message's params, or "".
func (e envelope) paramsSession() string { return sessionIDIn(e.Params) }

// resultSession is the sessionId in a response's result, or "".
func (e envelope) resultSession() string { return sessionIDIn(e.Result) }

func sessionIDIn(v jsontext.Value) string {
	if len(v) == 0 || v.Kind() != '{' {
		return ""
	}
	var s struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(v, &s)
	return s.SessionID
}

// writeSSE writes one message as an SSE data event.
func writeSSE(w io.Writer, msg jsontext.Value) error {
	// One data line per event: compact the payload in case it spans lines.
	compact := msg.Clone()
	if err := compact.Compact(); err != nil {
		return err
	}
	var b bytes.Buffer
	b.Grow(len(compact) + 8)
	b.WriteString("data: ")
	b.Write(compact)
	b.WriteString("\n\n")
	_, err := w.Write(b.Bytes())
	return err
}

// readSSE calls handle with the data of each event on r until r ends,
// ignoring comments and fields other than data.
func readSSE(r io.Reader, handle func(jsontext.Value)) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxMessageSize)
	var data []byte
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			if len(data) > 0 {
				handle(jsontext.Value(data))
				data = nil
			}
			continue
		}
		field, value, _ := bytes.Cut(line, []byte(":"))
		if string(field) != "data" {
			continue // a comment (": keepalive") or an unused field
		}
		value = bytes.TrimPrefix(value, []byte(" "))
		if data != nil {
			data = append(data, '\n')
		}
		data = append(data, value...)
	}
	return scanner.Err()
}
