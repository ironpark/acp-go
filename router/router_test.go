package router_test

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io"
	"strings"
	"testing"
	"time"

	acp "github.com/ironpark/acp-go"
	"github.com/ironpark/acp-go/acp1"
	"github.com/ironpark/acp-go/acp2"
	"github.com/ironpark/acp-go/router"
	schemav2 "github.com/ironpark/acp-go/schema/v2"
)

// v1Agent records the initialize request the router hands it.
type v1Agent struct {
	initialized chan *acp1.InitializeRequest
}

func (a *v1Agent) Initialize(_ context.Context, params *acp1.InitializeRequest) (*acp1.InitializeResponse, error) {
	a.initialized <- params
	return &acp1.InitializeResponse{ProtocolVersion: 1}, nil
}
func (a *v1Agent) Authenticate(context.Context, *acp1.AuthenticateRequest) (*acp1.AuthenticateResponse, error) {
	return nil, nil
}
func (a *v1Agent) NewSession(context.Context, *acp1.NewSessionRequest) (*acp1.NewSessionResponse, error) {
	return &acp1.NewSessionResponse{SessionID: "v1-session"}, nil
}
func (a *v1Agent) Prompt(context.Context, *acp1.PromptRequest) (*acp1.PromptResponse, error) {
	return nil, nil
}
func (a *v1Agent) CancelSession(context.Context, *acp1.CancelNotification) error { return nil }

type v2Agent struct {
	initialized chan *acp2.InitializeRequest
}

func (a *v2Agent) Initialize(_ context.Context, params *acp2.InitializeRequest) (*acp2.InitializeResponse, error) {
	a.initialized <- params
	return &acp2.InitializeResponse{ProtocolVersion: 2, Info: schemav2.Implementation{Name: "v2-agent", Version: "0"}}, nil
}
func (a *v2Agent) NewSession(context.Context, *acp2.NewSessionRequest) (*acp2.NewSessionResponse, error) {
	return &acp2.NewSessionResponse{SessionID: "v2-session"}, nil
}
func (a *v2Agent) Prompt(context.Context, *acp2.PromptRequest) (*acp2.PromptResponse, error) {
	return nil, nil
}
func (a *v2Agent) CancelSession(context.Context, *acp2.CancelSessionNotification) error { return nil }
func (a *v2Agent) ListSessions(context.Context, *acp2.ListSessionsRequest) (*acp2.ListSessionsResponse, error) {
	return &acp2.ListSessionsResponse{}, nil
}
func (a *v2Agent) ResumeSession(context.Context, *acp2.ResumeSessionRequest) (*acp2.ResumeSessionResponse, error) {
	return &acp2.ResumeSessionResponse{}, nil
}
func (a *v2Agent) CloseSession(context.Context, *acp2.CloseSessionRequest) (*acp2.CloseSessionResponse, error) {
	return &acp2.CloseSessionResponse{}, nil
}

// peer is a raw JSON-RPC client talking to a routed agent over pipes.
type peer struct {
	t      *testing.T
	out    *io.PipeWriter
	in     *bufio.Reader
	served chan error
}

func serve(t *testing.T, r *router.ProtocolRouter) *peer {
	t.Helper()
	agentIn, clientOut := io.Pipe()
	clientIn, agentOut := io.Pipe()
	p := &peer{t: t, out: clientOut, in: bufio.NewReader(clientIn), served: make(chan error, 1)}
	ctx, cancel := context.WithCancel(t.Context())
	go func() { p.served <- r.Serve(ctx, acp.NewStdioTransport(agentIn, agentOut)) }()
	t.Cleanup(func() {
		cancel()
		_ = clientOut.Close()
		_ = agentOut.Close()
	})
	return p
}

func (p *peer) send(line string) {
	p.t.Helper()
	if _, err := io.WriteString(p.out, line+"\n"); err != nil {
		p.t.Fatalf("write: %v", err)
	}
}

func (p *peer) receive() map[string]jsontext.Value {
	p.t.Helper()
	type read struct {
		line string
		err  error
	}
	ch := make(chan read, 1)
	go func() {
		line, err := p.in.ReadString('\n')
		ch <- read{line, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			p.t.Fatalf("read: %v", r.err)
		}
		var msg map[string]jsontext.Value
		if err := json.Unmarshal([]byte(r.line), &msg); err != nil {
			p.t.Fatalf("decode %q: %v", r.line, err)
		}
		return msg
	case <-time.After(2 * time.Second):
		p.t.Fatal("timed out waiting for a response")
		return nil
	}
}

// closedWithoutOutput reports whether the agent side hung up without writing.
func (p *peer) closedWithoutOutput() bool {
	p.t.Helper()
	select {
	case err := <-p.served:
		if err != nil {
			p.t.Errorf("Serve returned %v", err)
		}
	case <-time.After(2 * time.Second):
		p.t.Fatal("Serve did not return")
	}
	_ = p.out.Close()
	_, err := p.in.ReadByte()
	return err == io.EOF
}

func errorOf(t *testing.T, msg map[string]jsontext.Value) (int64, string) {
	t.Helper()
	var e struct {
		Code    int64  `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(msg["error"], &e); err != nil {
		t.Fatalf("no error object in %v", msg)
	}
	return e.Code, e.Message
}

func both() (*router.ProtocolRouter, *v1Agent, *v2Agent) {
	v1 := &v1Agent{initialized: make(chan *acp1.InitializeRequest, 1)}
	v2 := &v2Agent{initialized: make(chan *acp2.InitializeRequest, 1)}
	r := router.New().
		WithV1(func(*acp1.AgentSideConnection) acp1.Agent { return v1 }).
		WithV2(func(*acp2.AgentSideConnection) acp2.Agent { return v2 })
	return r, v1, v2
}

func TestV1ClientIsRoutedToV1(t *testing.T) {
	r, v1, _ := both()
	p := serve(t, r)

	p.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientInfo":{"name":"c","version":"0"}}}`)
	if got := string(p.receive()["result"]); !strings.Contains(got, `"protocolVersion":1`) {
		t.Errorf("result = %s", got)
	}
	if seen := <-v1.initialized; seen.ClientInfo == nil || seen.ClientInfo.Name != "c" {
		t.Errorf("v1 agent saw %+v", seen)
	}

	// Later messages pass through untouched.
	p.send(`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/tmp","mcpServers":[]}}`)
	if got := string(p.receive()["result"]); !strings.Contains(got, "v1-session") {
		t.Errorf("session/new result = %s", got)
	}
}

func TestV2ClientIsRoutedToV2(t *testing.T) {
	r, _, v2 := both()
	p := serve(t, r)

	p.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":2,"info":{"name":"c","version":"0"}}}`)
	if got := string(p.receive()["result"]); !strings.Contains(got, `"protocolVersion":2`) {
		t.Errorf("result = %s", got)
	}
	if seen := <-v2.initialized; seen.Info.Name != "c" || seen.ProtocolVersion != 2 {
		t.Errorf("v2 agent saw %+v", seen)
	}
}

func TestNewerVersionIsCappedAtV2(t *testing.T) {
	r, _, v2 := both()
	p := serve(t, r)

	p.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":3,"info":{"name":"c","version":"0"}}}`)
	p.receive()
	if seen := <-v2.initialized; seen.ProtocolVersion != 2 {
		t.Errorf("protocolVersion forwarded as %d, want 2", seen.ProtocolVersion)
	}
}

func TestV2ClientIsDowngradedForV1OnlyAgent(t *testing.T) {
	v1 := &v1Agent{initialized: make(chan *acp1.InitializeRequest, 1)}
	r := router.New().WithV1(func(*acp1.AgentSideConnection) acp1.Agent { return v1 })
	p := serve(t, r)

	p.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":2,"info":{"name":"c","version":"9"},"capabilities":{"auth":{"terminal":{}},"elicitation":{"form":{}}}}}`)
	if got := string(p.receive()["result"]); !strings.Contains(got, `"protocolVersion":1`) {
		t.Errorf("result = %s", got)
	}

	seen := <-v1.initialized
	if seen.ProtocolVersion != 1 {
		t.Errorf("protocolVersion = %d", seen.ProtocolVersion)
	}
	if seen.ClientInfo == nil || seen.ClientInfo.Name != "c" || seen.ClientInfo.Version != "9" {
		t.Errorf("clientInfo = %+v", seen.ClientInfo)
	}
	caps := seen.ClientCapabilities
	if caps == nil || caps.FS == nil || caps.FS.ReadTextFile == nil || *caps.FS.ReadTextFile || caps.FS.WriteTextFile == nil || *caps.FS.WriteTextFile {
		t.Errorf("fs capabilities = %+v, want explicit false", caps.FS)
	}
	if caps.Terminal == nil || *caps.Terminal {
		t.Errorf("terminal = %v, want false", caps.Terminal)
	}
	if !caps.GetAuth().GetTerminal() {
		t.Errorf("auth.terminal = %+v, want true", caps.Auth)
	}
	if caps.Elicitation == nil || caps.Elicitation.Form == nil {
		t.Errorf("elicitation = %+v, want form carried over", caps.Elicitation)
	}
	if caps.Session == nil || caps.Session.ConfigOptions == nil || caps.Session.ConfigOptions.Boolean == nil || caps.Plan == nil {
		t.Errorf("session/plan defaults missing: %+v", caps)
	}
}

func TestNewerVersionIsDowngradedForV1OnlyAgent(t *testing.T) {
	// A version above 2 is parsed in the v2 shape and downgraded the same way.
	v1 := &v1Agent{initialized: make(chan *acp1.InitializeRequest, 1)}
	p := serve(t, router.New().WithV1(func(*acp1.AgentSideConnection) acp1.Agent { return v1 }))

	p.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":3,"info":{"name":"c","version":"0"}}}`)
	if got := string(p.receive()["result"]); !strings.Contains(got, `"protocolVersion":1`) {
		t.Errorf("result = %s", got)
	}
	seen := <-v1.initialized
	if seen.ProtocolVersion != 1 || seen.ClientInfo == nil || seen.ClientInfo.Name != "c" {
		t.Errorf("v1 agent saw %+v", seen)
	}
}

func TestTerminalAuthMetaCannotBeDowngraded(t *testing.T) {
	v1 := &v1Agent{initialized: make(chan *acp1.InitializeRequest, 1)}
	p := serve(t, router.New().WithV1(func(*acp1.AgentSideConnection) acp1.Agent { return v1 }))

	p.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":2,"info":{"name":"c","version":"0"},"capabilities":{"auth":{"terminal":{"_meta":{"x":1}}}}}}`)
	if code, msg := errorOf(t, p.receive()); code != int64(acp.ErrorCodeInvalidParams) || !strings.Contains(msg, "cannot be represented in v1") {
		t.Errorf("error = %d %q", code, msg)
	}
}

func TestUnsupportedVersionIsRejected(t *testing.T) {
	v2 := &v2Agent{initialized: make(chan *acp2.InitializeRequest, 1)}
	p := serve(t, router.New().WithV2(func(*acp2.AgentSideConnection) acp2.Agent { return v2 }))

	p.send(`{"jsonrpc":"2.0","id":7,"method":"initialize","params":{"protocolVersion":1}}`)
	msg := p.receive()
	if string(msg["id"]) != "7" {
		t.Errorf("id = %s", msg["id"])
	}
	if code, text := errorOf(t, msg); code != int64(acp.ErrorCodeInvalidRequest) || !strings.Contains(text, "unsupported ACP protocol version 1; this endpoint supports ACP protocol version 2") {
		t.Errorf("error = %d %q", code, text)
	}
	if !p.closedWithoutOutput() {
		t.Error("connection stayed open after rejection")
	}
}

func TestFractionalVersionIsInvalidParams(t *testing.T) {
	r, _, _ := both()
	p := serve(t, r)
	p.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1.5}}`)
	if code, _ := errorOf(t, p.receive()); code != int64(acp.ErrorCodeInvalidParams) {
		t.Errorf("code = %d", code)
	}
}

func TestIntegralFloatVersionIsAccepted(t *testing.T) {
	r, v1, _ := both()
	p := serve(t, r)
	p.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1.0}}`)
	p.receive()
	if seen := <-v1.initialized; seen.ProtocolVersion != 1 {
		t.Errorf("protocolVersion = %d", seen.ProtocolVersion)
	}
}

func TestFirstRequestMustBeInitialize(t *testing.T) {
	r, _, _ := both()
	p := serve(t, r)
	p.send(`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/tmp","mcpServers":[]}}`)
	msg := p.receive()
	if code, text := errorOf(t, msg); code != int64(acp.ErrorCodeInvalidRequest) || !strings.Contains(text, "must be initialize") {
		t.Errorf("error = %d %q", code, text)
	}
	if !p.closedWithoutOutput() {
		t.Error("connection stayed open after rejection")
	}
}

func TestFirstNotificationClosesSilently(t *testing.T) {
	r, _, _ := both()
	p := serve(t, r)
	p.send(`{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s"}}`)
	if !p.closedWithoutOutput() {
		t.Error("expected the router to hang up without writing")
	}
}

func TestBatchIsRejected(t *testing.T) {
	r, _, _ := both()
	p := serve(t, r)
	p.send(`[{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}]`)
	msg := p.receive()
	if string(msg["id"]) != "null" {
		t.Errorf("id = %s, want null", msg["id"])
	}
	if code, _ := errorOf(t, msg); code != int64(acp.ErrorCodeInvalidRequest) {
		t.Errorf("code = %d", code)
	}
}

// TestRouterOptionsReachTheRoutedConnection checks that the options given to
// New configure the façade connection a client is routed to.
func TestRouterOptionsReachTheRoutedConnection(t *testing.T) {
	v1 := &v1Agent{initialized: make(chan *acp1.InitializeRequest, 1)}
	seen := make(chan string, 4)
	observe := acp.Middleware{Request: func(next acp.RequestHandler) acp.RequestHandler {
		return func(ctx context.Context, method string, params jsontext.Value) (any, error) {
			seen <- method
			return next(ctx, method, params)
		}
	}}
	r := router.New(acp.WithMiddleware(observe)).
		WithV1(func(*acp1.AgentSideConnection) acp1.Agent { return v1 })
	p := serve(t, r)
	p.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`)
	p.receive()
	if method := <-seen; method != "initialize" {
		t.Fatalf("middleware saw %q, want initialize", method)
	}
}
