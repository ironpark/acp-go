package acphttp

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	acp "github.com/ironpark/acp-go"
)

// fakeAgent answers initialize, session/new, session/load, session/resume and
// session/prompt over raw JSON-RPC; a prompt first sends a session/update for
// session s1.
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
		case "session/load", "session/resume":
			err = reply(`{}`)
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
		{"stream ahead of the session's load", "GET", ``, map[string]string{"Accept": "text/event-stream", ConnectionIDHeader: conn, SessionIDHeader: "zz"}, http.StatusOK},
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
	s1 := c.sessions["s1"]
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
	if c.sessions["s2"] == nil {
		t.Fatal("no provisional stream while loading")
	}
	if route(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s2"}}`) != c.connStream {
		t.Fatal("replay not on the connection stream")
	}
	if route(`{"jsonrpc":"2.0","id":3,"error":{"code":-32002,"message":"not found"}}`) != c.connStream {
		t.Fatal("load reply not on the connection stream")
	}
	// The load failed, so its provisional stream goes.
	if c.sessions["s2"] != nil {
		t.Fatal("failed load kept its provisional stream")
	}

	deliver(`{"jsonrpc":"2.0","id":4,"method":"session/load","params":{"sessionId":"s3"}}`)
	route(`{"jsonrpc":"2.0","id":4,"result":null}`) // a v1 load may answer null
	s3 := c.sessions["s3"]
	if s3 == nil || route(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s3"}}`) != s3 {
		t.Fatal("after a successful load, updates do not use the session stream")
	}

	// A resume attaches a session as a load does, though its reply names no
	// session: replay and reply on the connection stream, then the session's.
	deliver(`{"jsonrpc":"2.0","id":5,"method":"session/resume","params":{"sessionId":"s4"}}`)
	if c.hasEstablishedSession("s4") {
		t.Fatal("session established before its resume was answered")
	}
	if route(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s4"}}`) != c.connStream ||
		route(`{"jsonrpc":"2.0","id":5,"result":{}}`) != c.connStream {
		t.Fatal("resume replay or reply not on the connection stream")
	}
	if !c.hasEstablishedSession("s4") || route(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s4"}}`) != c.sessions["s4"] {
		t.Fatal("after a successful resume, updates do not use the session stream")
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

// rawPeer sends requests to the server as a client other than ClientTransport
// would, on the connection of an initialized transport.
type rawPeer struct {
	t      *testing.T
	url    string
	conn   string
	server *httpServerConn
}

func newRawPeer(t *testing.T) (rawPeer, *ClientTransport) {
	t.Helper()
	server, client, ts := newHTTPPair(t)
	call(t, client, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	expect(t, client, `"result"`)
	waitAttached(t, server)
	client.mu.Lock()
	conn := client.connectionID
	client.mu.Unlock()
	server.mu.Lock()
	defer server.mu.Unlock()
	return rawPeer{t, ts.URL, conn, server.conns[conn]}, client
}

func (p rawPeer) do(ctx context.Context, method, session, body string) *http.Response {
	p.t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, p.url, strings.NewReader(body))
	if err != nil {
		p.t.Fatal(err)
	}
	req.Header.Set(ConnectionIDHeader, p.conn)
	if session != "" {
		req.Header.Set(SessionIDHeader, session)
	}
	if method == http.MethodGet {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	return resp
}

// post sends one message with the session header and returns the status.
func (p rawPeer) post(session, body string) int {
	p.t.Helper()
	resp := p.do(p.t.Context(), http.MethodPost, session, body)
	resp.Body.Close()
	return resp.StatusCode
}

// sessions counts the session streams the connection holds, and how many of
// them are provisional.
func (p rawPeer) sessions() (total, provisional int) {
	p.server.mu.Lock()
	defer p.server.mu.Unlock()
	return len(p.server.sessions), len(p.server.provisional)
}

// The RFD's reconnect flow, which the TypeScript SDK follows: open the stream
// of a remembered session, then send session/load or session/resume with
// Acp-Session-Id. The session takes prompts once the agent has answered.
func TestSessionStreamBeforeReattach(t *testing.T) {
	for _, method := range []string{"session/load", "session/resume"} {
		t.Run(method, func(t *testing.T) {
			peer, client := newRawPeer(t)
			const session = "s1" // the session fakeAgent's prompt updates name

			sse := peer.do(t.Context(), http.MethodGet, session, "")
			defer sse.Body.Close()
			if sse.StatusCode != http.StatusOK {
				t.Fatalf("session stream before %s: status %d, want 200", method, sse.StatusCode)
			}
			prompt := `{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"s1","prompt":[]}}`
			if status := peer.post(session, prompt); status != http.StatusNotFound {
				t.Fatalf("prompt before %s: status %d, want 404", method, status)
			}

			reattach := `{"jsonrpc":"2.0","id":2,"method":"` + method + `","params":{"sessionId":"s1","cwd":"/"}}`
			if status := peer.post(session, reattach); status != http.StatusAccepted {
				t.Fatalf("%s with the session header: status %d, want 202", method, status)
			}
			expect(t, client, `"id":2`) // answered on the connection stream

			if status := peer.post(session, prompt); status != http.StatusAccepted {
				t.Fatalf("prompt after %s: status %d, want 202", method, status)
			}
			// The stream opened ahead of the reattach is the session's: the
			// prompt's update and reply arrive on it.
			events := make(chan string, 1)
			go func() {
				buf := make([]byte, 4096)
				n, _ := sse.Body.Read(buf)
				events <- string(buf[:n])
			}()
			select {
			case event := <-events:
				if !strings.Contains(event, "session/update") {
					t.Fatalf("session stream delivered %q, want the prompt's update", event)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("nothing arrived on the session stream opened before the reattach")
			}
		})
	}
}

// ClientTransport resumes a session and then prompts it: the session stream
// it opens after the answer carries the prompt's update and reply.
func TestClientResumesThenPrompts(t *testing.T) {
	_, client, _ := newHTTPPair(t)
	call(t, client, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	expect(t, client, `"result"`)
	call(t, client, `{"jsonrpc":"2.0","id":2,"method":"session/resume","params":{"sessionId":"s1","cwd":"/"}}`)
	expect(t, client, `"id":2`)
	call(t, client, `{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"s1","prompt":[]}}`)
	expect(t, client, `session/update`)
	expect(t, client, `end_turn`)
}

// A stream opened for a session that is never loaded goes with its GET, and
// a connection holds only so many of them at once.
func TestProvisionalStreamsAreBounded(t *testing.T) {
	peer, _ := newRawPeer(t)

	ctx, cancel := context.WithCancel(t.Context())
	sse := peer.do(ctx, http.MethodGet, "never-loaded", "")
	if total, provisional := peer.sessions(); sse.StatusCode != http.StatusOK || total != 1 || provisional != 1 {
		t.Fatalf("status %d, %d sessions (%d provisional), want 200 and one provisional", sse.StatusCode, total, provisional)
	}
	cancel()
	sse.Body.Close()
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(time.Millisecond) {
		if total, _ := peer.sessions(); total == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the provisional stream outlived its GET")
		}
	}

	for i := range maxProvisionalStreams {
		resp := peer.do(t.Context(), http.MethodGet, fmt.Sprintf("pending-%d", i), "")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("stream %d: status %d, want 200", i, resp.StatusCode)
		}
	}
	over := peer.do(t.Context(), http.MethodGet, "one-too-many", "")
	over.Body.Close()
	if over.StatusCode != http.StatusConflict {
		t.Fatalf("stream past the limit: status %d, want 409", over.StatusCode)
	}
	// A session the agent attaches is not provisional and still gets through.
	if status := peer.post("", `{"jsonrpc":"2.0","id":2,"method":"session/load","params":{"sessionId":"real"}}`); status != http.StatusAccepted {
		t.Fatalf("session/load at the limit: status %d, want 202", status)
	}
}
