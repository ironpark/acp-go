package acp1_test

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"testing"
	"time"

	acp "github.com/ironpark/acp-go"
	"github.com/ironpark/acp-go/acp1"
	"github.com/ironpark/acp-go/acp1/acp1test"
	schema "github.com/ironpark/acp-go/schema/v1"
)

// testAgent implements the required Agent methods plus the optional terminal
// and extension hooks the tests exercise. Session lifecycle comes from the
// embedded manager.
type testAgent struct {
	*acp1.SessionManager[*testSession]
	client acp1.Client

	cancelled chan acp1.SessionID
}

type testSession struct{ cwd string }

func (s *testSession) SessionInfo() acp1.SessionInfo { return acp1.SessionInfo{Cwd: s.cwd} }

// SessionModes makes the manager report the session's modes.
func (s *testSession) SessionModes() *acp1.SessionModeState {
	return &acp1.SessionModeState{CurrentModeID: "ask", AvailableModes: []acp1.SessionMode{{ID: "ask", Name: "Ask"}}}
}

func newTestAgent() *testAgent {
	return &testAgent{
		SessionManager: acp1.NewSessionManager(
			acp1.NewMemoryStore[*testSession](),
			func(_ context.Context, params *acp1.NewSessionRequest) (acp1.SessionID, *testSession, error) {
				return acp1.GenerateSessionID(), &testSession{cwd: params.Cwd}, nil
			},
		),
		cancelled: make(chan acp1.SessionID, 1),
	}
}

func (a *testAgent) Initialize(context.Context, *acp1.InitializeRequest) (*acp1.InitializeResponse, error) {
	return &acp1.InitializeResponse{ProtocolVersion: acp1.ProtocolVersion}, nil
}

func (a *testAgent) ListSessions(ctx context.Context, params *acp1.ListSessionsRequest) (*acp1.ListSessionsResponse, error) {
	return a.List(ctx, params)
}

func (a *testAgent) Authenticate(context.Context, *acp1.AuthenticateRequest) (*acp1.AuthenticateResponse, error) {
	return &acp1.AuthenticateResponse{}, nil
}

func (a *testAgent) Prompt(ctx context.Context, params *acp1.PromptRequest) (*acp1.PromptResponse, error) {
	stream := acp1.NewSessionStream(a.client, params.SessionID)
	if err := stream.SendText(ctx, "hello", acp1.WithMessageID("msg_1")); err != nil {
		return nil, err
	}
	return &acp1.PromptResponse{StopReason: schema.StopReasonEndTurn}, nil
}

func (a *testAgent) CancelSession(_ context.Context, params *acp1.CancelNotification) error {
	a.cancelled <- params.SessionID
	return nil
}

func (a *testAgent) ServeExtMethod(_ context.Context, method string, params jsontext.Value) (any, error) {
	return map[string]string{"method": method, "params": string(params)}, nil
}

// testClient records the updates it receives and always allows tool calls.
type testClient struct {
	updates chan *acp1.SessionNotification
}

func newTestClient() *testClient {
	return &testClient{updates: make(chan *acp1.SessionNotification, 16)}
}

func (c *testClient) SessionUpdate(_ context.Context, params *acp1.SessionNotification) error {
	c.updates <- params
	return nil
}

func (c *testClient) RequestPermission(_ context.Context, params *acp1.RequestPermissionRequest) (*acp1.RequestPermissionResponse, error) {
	return &acp1.RequestPermissionResponse{
		Outcome: schema.NewRequestPermissionOutcome(schema.RequestPermissionOutcomeSelected{
			OptionID: params.Options[0].OptionID,
		}),
	}, nil
}

func (c *testClient) ReadTextFile(_ context.Context, params *acp1.ReadTextFileRequest) (*acp1.ReadTextFileResponse, error) {
	return &acp1.ReadTextFileResponse{Content: "contents of " + params.Path}, nil
}

// connect wires an agent and a client together over in-memory pipes.
func connect(t *testing.T, agent *testAgent, client acp1.Client) (*acp1.ClientSideConnection, *acp1.AgentSideConnection) {
	t.Helper()
	var agentConn *acp1.AgentSideConnection
	clientConn := acp1test.Connect(t, func(c *acp1.AgentSideConnection) acp1.Agent {
		agentConn, agent.client = c, c
		return agent
	}, client)
	return clientConn, agentConn
}

func TestPromptTurn(t *testing.T) {
	client := newTestClient()
	conn, _ := connect(t, newTestAgent(), client)
	ctx := t.Context()

	initialized, err := conn.Initialize(ctx, &acp1.InitializeRequest{ProtocolVersion: acp1.ProtocolVersion})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if initialized.ProtocolVersion != acp1.ProtocolVersion {
		t.Errorf("protocol version = %d, want %d", initialized.ProtocolVersion, acp1.ProtocolVersion)
	}

	created, err := conn.NewSession(ctx, &acp1.NewSessionRequest{Cwd: "/tmp", MCPServers: []schema.MCPServer{}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	result, err := conn.Prompt(ctx, &acp1.PromptRequest{
		SessionID: created.SessionID,
		Prompt:    []acp1.ContentBlock{schema.NewContentBlock(schema.ContentBlockText{Text: "hi"})},
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if result.StopReason != schema.StopReasonEndTurn {
		t.Errorf("stop reason = %q, want %q", result.StopReason, schema.StopReasonEndTurn)
	}

	select {
	case update := <-client.updates:
		chunk, ok := update.Update.Variant().(schema.SessionUpdateAgentMessageChunk)
		if !ok {
			t.Fatalf("update variant = %T, want an agent message chunk", update.Update.Variant())
		}
		text, ok := chunk.Content.Variant().(schema.ContentBlockText)
		if !ok || text.Text != "hello" {
			t.Errorf("chunk content = %#v", chunk.Content.Variant())
		}
		if chunk.MessageID == nil || *chunk.MessageID != "msg_1" {
			t.Errorf("chunk message id = %v, want msg_1", chunk.MessageID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no session update arrived")
	}
}

func TestVoidResponseDecodes(t *testing.T) {
	conn, _ := connect(t, newTestAgent(), newTestClient())
	if _, err := conn.Authenticate(t.Context(), &acp1.AuthenticateRequest{MethodID: "none"}); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
}

func TestSessionManagerServesLifecycleMethods(t *testing.T) {
	conn, _ := connect(t, newTestAgent(), newTestClient())
	ctx := t.Context()

	created, err := conn.NewSession(ctx, &acp1.NewSessionRequest{Cwd: "/tmp", MCPServers: []schema.MCPServer{}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if created.Modes == nil || created.Modes.CurrentModeID != "ask" {
		t.Errorf("NewSession modes = %+v, want the session's", created.Modes)
	}
	if _, err := conn.NewSession(ctx, &acp1.NewSessionRequest{Cwd: "/elsewhere", MCPServers: []schema.MCPServer{}}); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	listed, err := conn.ListSessions(ctx, &acp1.ListSessionsRequest{Cwd: new("/tmp")})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(listed.Sessions) != 1 || listed.Sessions[0].SessionID != created.SessionID || listed.Sessions[0].Cwd != "/tmp" {
		t.Fatalf("listed sessions in /tmp = %#v", listed.Sessions)
	}
	// Closing ends the active session but keeps it resumable.
	if _, err := conn.CloseSession(ctx, &acp1.CloseSessionRequest{SessionID: created.SessionID}); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	resumed, err := conn.ResumeSession(ctx, &acp1.ResumeSessionRequest{SessionID: created.SessionID, Cwd: "/tmp"})
	if err != nil {
		t.Fatalf("ResumeSession after close: %v", err)
	}
	if resumed.Modes == nil || resumed.Modes.CurrentModeID != "ask" {
		t.Errorf("ResumeSession modes = %+v, want the session's", resumed.Modes)
	}
	if _, err := conn.DeleteSession(ctx, &acp1.DeleteSessionRequest{SessionID: created.SessionID}); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := conn.ResumeSession(ctx, &acp1.ResumeSessionRequest{SessionID: created.SessionID, Cwd: "/tmp"}); !acp.IsCode(err, acp.ErrorCodeResourceNotFound) {
		t.Errorf("ResumeSession after delete: %v, want a resource-not-found error", err)
	}
	// Deleting an unknown session succeeds.
	if _, err := conn.DeleteSession(ctx, &acp1.DeleteSessionRequest{SessionID: created.SessionID}); err != nil {
		t.Errorf("second delete: %v, want success", err)
	}
}

func TestUnimplementedOptionalMethodIsMethodNotFound(t *testing.T) {
	conn, _ := connect(t, newTestAgent(), newTestClient())
	_, err := conn.SetSessionMode(t.Context(), &acp1.SetSessionModeRequest{
		SessionID: "session_1",
		ModeID:    "ask",
	})
	if !acp.IsCode(err, acp.ErrorCodeMethodNotFound) {
		t.Errorf("SetSessionMode error = %v, want method not found", err)
	}
}

func TestAuthenticateIsOptional(t *testing.T) {
	_, conn := acp1.Pipe(t.Context(), func(*acp1.AgentSideConnection) acp1.Agent { return bareAgent{} },
		func(*acp1.ClientSideConnection) acp1.Client { return newTestClient() })
	_, err := conn.Authenticate(t.Context(), &acp1.AuthenticateRequest{MethodID: "none"})
	if !acp.IsCode(err, acp.ErrorCodeMethodNotFound) {
		t.Errorf("Authenticate error = %v, want method not found", err)
	}
}

func TestInvalidParamsAreRejectedBeforeTheHandler(t *testing.T) {
	conn, _ := connect(t, newTestAgent(), newTestClient())
	// sessionId is required, so validation fails before Prompt is called.
	_, err := conn.ExtMethod(t.Context(), schema.AgentMethodsSessionPrompt, map[string]any{"prompt": []any{}})
	if !acp.IsCode(err, acp.ErrorCodeInvalidParams) {
		t.Errorf("error = %v, want invalid params", err)
	}
}

func TestExtensionMethodsFallThrough(t *testing.T) {
	conn, _ := connect(t, newTestAgent(), newTestClient())
	raw, err := conn.ExtMethod(t.Context(), "example.com/ping", map[string]string{"hello": "world"})
	if err != nil {
		t.Fatalf("ExtMethod: %v", err)
	}
	var echoed struct {
		Method string `json:"method"`
		Params string `json:"params"`
	}
	if err := json.Unmarshal(raw, &echoed); err != nil {
		t.Fatalf("decode ext response: %v", err)
	}
	if echoed.Method != "example.com/ping" || echoed.Params != `{"hello":"world"}` {
		t.Errorf("ext method echoed %#v", echoed)
	}
}

func TestCancelNotificationReachesTheAgent(t *testing.T) {
	agent := newTestAgent()
	conn, _ := connect(t, agent, newTestClient())

	if err := conn.CancelSession(t.Context(), &acp1.CancelNotification{SessionID: "session_1"}); err != nil {
		t.Fatalf("CancelSession: %v", err)
	}
	select {
	case id := <-agent.cancelled:
		if id != "session_1" {
			t.Errorf("cancelled session = %q", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("agent never saw the cancel notification")
	}
}

func TestAgentCallsBackIntoTheClient(t *testing.T) {
	agent := newTestAgent()
	_, agentConn := connect(t, agent, newTestClient())
	ctx := t.Context()

	read, err := agentConn.ReadTextFile(ctx, &acp1.ReadTextFileRequest{SessionID: "session_1", Path: "/etc/hosts"})
	if err != nil {
		t.Fatalf("ReadTextFile: %v", err)
	}
	if read.Content != "contents of /etc/hosts" {
		t.Errorf("content = %q", read.Content)
	}

	granted, err := agentConn.RequestPermission(ctx, &acp1.RequestPermissionRequest{
		SessionID: "session_1",
		ToolCall:  schema.ToolCallUpdate{ToolCallID: "call_1"},
		Options: []schema.PermissionOption{
			{OptionID: "allow", Name: "Allow", Kind: schema.PermissionOptionKindAllowOnce},
		},
	})
	if err != nil {
		t.Fatalf("RequestPermission: %v", err)
	}
	selected, ok := granted.Outcome.Variant().(schema.RequestPermissionOutcomeSelected)
	if !ok || selected.OptionID != "allow" {
		t.Errorf("outcome = %#v", granted.Outcome.Variant())
	}
}

func TestUnsupportedClientMethodIsMethodNotFound(t *testing.T) {
	// testClient implements neither FileWriter nor TerminalHandler.
	_, agentConn := connect(t, newTestAgent(), newTestClient())
	_, err := agentConn.WriteTextFile(t.Context(), &acp1.WriteTextFileRequest{
		SessionID: "session_1",
		Path:      "/tmp/file",
		Content:   "data",
	})
	if !acp.IsCode(err, acp.ErrorCodeMethodNotFound) {
		t.Errorf("WriteTextFile error = %v, want method not found", err)
	}
}

// An agent that embeds both acp.ExtRouter and its connection still serves
// extension methods: the handler methods and the connection's senders have
// different names, so neither selector is ambiguous.
type extRouterAgent struct {
	acp.ExtRouter
	*acp1.AgentSideConnection
}

var (
	_ acp1.ExtMethodHandler       = (*extRouterAgent)(nil)
	_ acp1.ExtNotificationHandler = (*extRouterAgent)(nil)
)
