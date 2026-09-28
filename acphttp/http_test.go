package acphttp

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	acp "github.com/ironpark/acp-go"
)

// fakeAgent answers initialize, session/new and session/prompt over raw
// JSON-RPC; a prompt first sends a session/update for its session.
func fakeAgent(ctx context.Context, t acp.Transport) error {
	for {
		msg, err := t.ReadMessage(ctx)
		if err != nil {
			return err
		}
		e, err := parseEnvelope(msg)
		if err != nil || !e.isRequest() {
			continue
		}
		reply := func(result string) error {
			return t.WriteMessage(ctx, jsontext.Value(`{"jsonrpc":"2.0","id":`+string(e.ID)+`,"result":`+result+`}`))
		}
		switch e.Method {
		case "initialize":
			err = reply(`{"protocolVersion":1}`)
		case "session/new":
			err = reply(`{"sessionId":"s1"}`)
		case "session/prompt":
			update := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{}}}`
			if err = t.WriteMessage(ctx, jsontext.Value(update)); err == nil {
				err = reply(`{"stopReason":"end_turn"}`)
			}
		}
		if err != nil {
			return err
		}
	}
}

func newHTTPPair(t *testing.T, opts ...ServerOption) (*Server, *ClientTransport, *httptest.Server) {
	t.Helper()
	server := NewServer(fakeAgent, opts...)
	ts := httptest.NewServer(server)
	client := NewClientTransport(ts.URL)
	t.Cleanup(func() {
		client.Close()
		server.Close()
		ts.Close()
	})
	return server, client, ts
}

// waitAttached waits until the client's connection stream is open on server.
func waitAttached(t *testing.T, server *Server) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		server.mu.Lock()
		attached := false
		for _, c := range server.conns {
			c.mu.Lock()
			attached = c.readers > 0
			c.mu.Unlock()
		}
		server.mu.Unlock()
		if attached {
			return
		}
	}
	t.Fatal("the connection stream never opened")
}

func readWithin(t *testing.T, tr acp.Transport) (jsontext.Value, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	return tr.ReadMessage(ctx)
}

func call(t *testing.T, client *ClientTransport, msg string) {
	t.Helper()
	if err := client.WriteMessage(t.Context(), jsontext.Value(msg)); err != nil {
		t.Fatalf("send %s: %v", msg, err)
	}
}

func expect(t *testing.T, client *ClientTransport, contains string) {
	t.Helper()
	got, err := readWithin(t, client)
	if err != nil || !strings.Contains(string(got), contains) {
		t.Fatalf("read %s, %v; want a message containing %s", got, err, contains)
	}
}

func TestHTTPTransportConversation(t *testing.T) {
	_, client, _ := newHTTPPair(t)

	call(t, client, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`)
	expect(t, client, `"protocolVersion":1`)
	call(t, client, `{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/"}}`)
	expect(t, client, `"sessionId":"s1"`)
	call(t, client, `{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"s1","prompt":[]}}`)
	expect(t, client, `"method":"session/update"`)
	expect(t, client, `"stopReason":"end_turn"`)
}

func TestHTTPClientInitializesOnce(t *testing.T) {
	_, client, _ := newHTTPPair(t)
	call(t, client, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	expect(t, client, `"result"`)
	if err := client.WriteMessage(t.Context(), jsontext.Value(`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{}}`)); err == nil {
		t.Fatal("a second initialize on the same transport succeeded")
	}
}

func TestHTTPClientSendsBeforeInitialize(t *testing.T) {
	_, client, _ := newHTTPPair(t)
	err := client.WriteMessage(t.Context(), jsontext.Value(`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{}}`))
	if err == nil {
		t.Fatal("session/new before initialize succeeded")
	}
}

func TestHTTPClientEOFWhenServerCloses(t *testing.T) {
	server, client, _ := newHTTPPair(t)
	call(t, client, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	expect(t, client, `"result"`)

	waitAttached(t, server)
	server.Close() // ends the connection stream
	if _, err := readWithin(t, client); !errors.Is(err, io.EOF) {
		t.Fatalf("after the server closed, ReadMessage = %v, want io.EOF", err)
	}
}

func TestHTTPClientCloseDeletesConnection(t *testing.T) {
	server, client, _ := newHTTPPair(t)
	call(t, client, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	expect(t, client, `"result"`)

	client.Close()
	server.mu.Lock()
	open := len(server.conns)
	server.mu.Unlock()
	if open != 0 {
		t.Fatalf("%d connections left after the client closed", open)
	}
	if _, err := readWithin(t, client); !errors.Is(err, io.EOF) {
		t.Fatalf("ReadMessage after Close = %v, want io.EOF", err)
	}
	if err := client.WriteMessage(t.Context(), jsontext.Value(`{}`)); !errors.Is(err, ErrTransportClosed) {
		t.Fatalf("WriteMessage after Close = %v, want ErrTransportClosed", err)
	}
}

func TestHTTPServerRejects(t *testing.T) {
	server, client, ts := newHTTPPair(t)
	call(t, client, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	expect(t, client, `"result"`)
	waitAttached(t, server)
	client.mu.Lock()
	conn := client.connectionID
	client.mu.Unlock()

	tests := []struct {
		name, method, body string
		header             map[string]string
		want               int
	}{
		{"not JSON", "POST", `{}`, map[string]string{"Content-Type": "text/plain"}, http.StatusUnsupportedMediaType},
		{"batch", "POST", `[]`, map[string]string{"Content-Type": "application/json"}, http.StatusNotImplemented},
		{"no connection", "POST", `{"jsonrpc":"2.0","method":"x"}`, map[string]string{"Content-Type": "application/json"}, http.StatusBadRequest},
		{"unknown connection", "POST", `{"jsonrpc":"2.0","method":"x"}`, map[string]string{"Content-Type": "application/json", ConnectionIDHeader: "nope"}, http.StatusNotFound},
		{"prompt without session header", "POST", `{"jsonrpc":"2.0","id":9,"method":"session/prompt","params":{"sessionId":"s1"}}`, map[string]string{"Content-Type": "application/json", ConnectionIDHeader: conn}, http.StatusBadRequest},
		{"unknown session", "POST", `{"jsonrpc":"2.0","id":9,"method":"session/prompt","params":{"sessionId":"zz"}}`, map[string]string{"Content-Type": "application/json", ConnectionIDHeader: conn, SessionIDHeader: "zz"}, http.StatusNotFound},
		{"stream without Accept", "GET", ``, map[string]string{ConnectionIDHeader: conn}, http.StatusNotAcceptable},
		{"stream of unknown session", "GET", ``, map[string]string{"Accept": "text/event-stream", ConnectionIDHeader: conn, SessionIDHeader: "zz"}, http.StatusNotFound},
		{"delete without connection", "DELETE", ``, nil, http.StatusBadRequest},
		{"put", "PUT", ``, nil, http.StatusMethodNotAllowed},
	}
	for _, tt := range tests {
		req, _ := http.NewRequestWithContext(t.Context(), tt.method, ts.URL, strings.NewReader(tt.body))
		for k, v := range tt.header {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		resp.Body.Close()
		if resp.StatusCode != tt.want {
			t.Errorf("%s: status %d, want %d", tt.name, resp.StatusCode, tt.want)
		}
	}
}

// route decides the stream of each agent message; these cases follow the
// RFD and the Python SDK's routing.
func TestHTTPServerRouting(t *testing.T) {
	c := newHTTPServerConn("c1", jsonrpcKey(`0`))
	deliver := func(msg string) {
		e, _ := parseEnvelope(jsontext.Value(msg))
		go c.deliver(t.Context(), jsontext.Value(msg), e)
		<-c.incoming
	}
	route := func(msg string) *outbound {
		e, _ := parseEnvelope(jsontext.Value(msg))
		return c.route(e)
	}

	// session/new's reply names the session: connection stream, and the
	// session's stream now exists for the client to open.
	deliver(`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{}}`)
	if route(`{"jsonrpc":"2.0","id":1,"result":{"sessionId":"s1"}}`) != c.connStream {
		t.Fatal("session/new reply not on the connection stream")
	}
	s1 := c.stream("s1")
	if s1 == nil {
		t.Fatal("no stream for the new session")
	}

	// A prompt's updates, requests to the client and reply use its session.
	deliver(`{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"s1"}}`)
	for _, msg := range []string{
		`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1"}}`,
		`{"jsonrpc":"2.0","id":7,"method":"session/request_permission","params":{"sessionId":"s1"}}`,
		`{"jsonrpc":"2.0","id":2,"result":{"stopReason":"end_turn"}}`,
	} {
		if route(msg) != s1 {
			t.Fatalf("%s not on the session stream", msg)
		}
	}

	// Messages without a session go to the connection stream.
	if route(`{"jsonrpc":"2.0","method":"_x/ping","params":{}}`) != c.connStream {
		t.Fatal("sessionless notification not on the connection stream")
	}

	// A load keeps its replay on the connection stream until it is answered.
	deliver(`{"jsonrpc":"2.0","id":3,"method":"session/load","params":{"sessionId":"s2"}}`)
	if c.stream("s2") == nil {
		t.Fatal("no provisional stream while loading")
	}
	if route(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s2"}}`) != c.connStream {
		t.Fatal("replay not on the connection stream")
	}
	if route(`{"jsonrpc":"2.0","id":3,"error":{"code":-32002,"message":"not found"}}`) != c.connStream {
		t.Fatal("load reply not on the connection stream")
	}
	// The load failed, so its provisional stream goes.
	if c.stream("s2") != nil {
		t.Fatal("failed load kept its provisional stream")
	}

	deliver(`{"jsonrpc":"2.0","id":4,"method":"session/load","params":{"sessionId":"s3"}}`)
	route(`{"jsonrpc":"2.0","id":4,"result":null}`) // a v1 load may answer null
	s3 := c.stream("s3")
	if s3 == nil || route(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s3"}}`) != s3 {
		t.Fatal("after a successful load, updates do not use the session stream")
	}
}

func jsonrpcKey(id string) string {
	e, _ := parseEnvelope(jsontext.Value(`{"id":` + id + `}`))
	return e.idKey()
}

// TestClientCloseAbortsAPost: a connection flushing on its way out writes
// without cancellation, so Close must end a POST the server never answers.
func TestClientCloseAbortsAPost(t *testing.T) {
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.Header.Get(ConnectionIDHeader) == "":
			w.Header().Set(ConnectionIDHeader, "c1")
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1}}`)
		case r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "text/event-stream")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		case r.Method == http.MethodPost:
			<-release // a server that never answers
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer ts.Close()
	defer close(release)
	client := NewClientTransport(ts.URL)
	defer client.Close()
	if err := client.WriteMessage(t.Context(), jsontext.Value(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)); err != nil {
		t.Fatal(err)
	}

	written := make(chan error, 1)
	go func() {
		ctx := context.WithoutCancel(t.Context())
		written <- client.WriteMessage(ctx, jsontext.Value(`{"jsonrpc":"2.0","method":"x"}`))
	}()
	time.Sleep(50 * time.Millisecond) // let the POST reach the server
	client.Close()
	select {
	case err := <-written:
		if err == nil {
			t.Error("WriteMessage = nil for a POST the server never answered")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close left a POST in flight")
	}
}
