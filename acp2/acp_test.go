package acp2_test

import (
	"context"
	"testing"
	"time"

	acp "github.com/ironpark/acp-go"
	"github.com/ironpark/acp-go/acp2"
	"github.com/ironpark/acp-go/acp2/acp2test"
	schema "github.com/ironpark/acp-go/schema/v2"
)

type testSession struct{ cwd acp2.AbsolutePath }

func (s *testSession) SessionInfo() acp2.SessionInfo { return acp2.SessionInfo{Cwd: s.cwd} }

// testAgent implements the required Agent methods plus MCP message handling,
// so the request-versus-notification split of mcp/message is exercised.
type testAgent struct {
	*acp2.SessionManager[*testSession]
	client acp2.Client

	cancelled   chan acp2.SessionID
	mcpNotified chan string
	initialized chan *acp2.InitializeRequest
}

func newTestAgent() *testAgent {
	return &testAgent{
		SessionManager: acp2.NewSessionManager(
			acp2.NewMemoryStore[*testSession](),
			func(_ context.Context, params *acp2.NewSessionRequest) (acp2.SessionID, *testSession, error) {
				return acp2.GenerateSessionID(), &testSession{cwd: params.Cwd}, nil
			},
		),
		cancelled:   make(chan acp2.SessionID, 1),
		mcpNotified: make(chan string, 1),
		initialized: make(chan *acp2.InitializeRequest, 1),
	}
}

func (a *testAgent) Initialize(_ context.Context, params *acp2.InitializeRequest) (*acp2.InitializeResponse, error) {
	a.initialized <- params
	return &acp2.InitializeResponse{
		ProtocolVersion: acp2.ProtocolVersion,
		Info:            schema.Implementation{Name: "test-agent", Version: "0"},
	}, nil
}

func (a *testAgent) Prompt(ctx context.Context, params *acp2.PromptRequest) (*acp2.PromptResponse, error) {
	err := a.client.SessionUpdate(ctx, &acp2.UpdateSessionNotification{
		SessionID: params.SessionID,
		Update: schema.NewSessionUpdate(schema.SessionUpdateAgentMessageChunk{
			MessageID: "msg_1",
			Content:   schema.NewContentBlock(schema.ContentBlockText{Text: "hello"}),
		}),
	})
	if err != nil {
		return nil, err
	}
	return &acp2.PromptResponse{MessageID: "msg_1"}, nil
}

func (a *testAgent) CancelSession(_ context.Context, params *acp2.CancelSessionNotification) error {
	a.cancelled <- params.SessionID
	return nil
}

func (a *testAgent) MessageMCP(_ context.Context, params *acp2.MessageMCPRequest) (*acp2.MessageMCPResponse, error) {
	result := acp2.MessageMCPResponse(`{"echo":"` + params.Method + `"}`)
	return &result, nil
}

func (a *testAgent) NotifyMCP(_ context.Context, params *acp2.MessageMCPNotification) error {
	a.mcpNotified <- params.Method
	return nil
}

type testClient struct {
	updates chan *acp2.UpdateSessionNotification
}

func newTestClient() *testClient {
	return &testClient{updates: make(chan *acp2.UpdateSessionNotification, 16)}
}

func (c *testClient) SessionUpdate(_ context.Context, params *acp2.UpdateSessionNotification) error {
	c.updates <- params
	return nil
}

func (c *testClient) RequestPermission(_ context.Context, params *acp2.RequestPermissionRequest) (*acp2.RequestPermissionResponse, error) {
	return &acp2.RequestPermissionResponse{
		Outcome: schema.NewRequestPermissionOutcome(schema.RequestPermissionOutcomeSelected{
			OptionID: params.Options[0].OptionID,
		}),
	}, nil
}

func connect(t *testing.T, agent *testAgent, client acp2.Client) (*acp2.ClientSideConnection, *acp2.AgentSideConnection) {
	t.Helper()
	var agentConn *acp2.AgentSideConnection
	clientConn := acp2test.Connect(t, func(c *acp2.AgentSideConnection) acp2.Agent {
		agentConn, agent.client = c, c
		return agent
	}, client)
	return clientConn, agentConn
}

func TestPromptTurn(t *testing.T) {
	agent := newTestAgent()
	client := newTestClient()
	conn, _ := connect(t, agent, client)
	ctx := t.Context()

	initialized, err := conn.Initialize(ctx, &acp2.InitializeRequest{
		ProtocolVersion: acp2.ProtocolVersion,
		Info:            schema.Implementation{Name: "test-client", Version: "0"},
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if initialized.ProtocolVersion != acp2.ProtocolVersion || initialized.Info.Name != "test-agent" {
		t.Errorf("initialize response = %+v", initialized)
	}
	if seen := <-agent.initialized; seen.Info.Name != "test-client" {
		t.Errorf("agent saw client info %+v", seen.Info)
	}

	created, err := conn.NewSession(ctx, &acp2.NewSessionRequest{Cwd: "/tmp"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	result, err := conn.Prompt(ctx, &acp2.PromptRequest{
		SessionID: created.SessionID,
		Prompt:    []acp2.ContentBlock{schema.NewContentBlock(schema.ContentBlockText{Text: "hi"})},
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if result.MessageID != "msg_1" {
		t.Errorf("message id = %q", result.MessageID)
	}

	select {
	case update := <-client.updates:
		chunk, ok := update.Update.Variant().(schema.SessionUpdateAgentMessageChunk)
		if !ok {
			t.Fatalf("update variant = %T", update.Update.Variant())
		}
		if text, ok := chunk.Content.Variant().(schema.ContentBlockText); !ok || text.Text != "hello" {
			t.Errorf("chunk content = %#v", chunk.Content.Variant())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no session update arrived")
	}
}

func TestMCPMessageRequestAndNotificationAreSplit(t *testing.T) {
	agent := newTestAgent()
	conn, _ := connect(t, agent, newTestClient())
	ctx := t.Context()

	result, err := conn.MessageMCP(ctx, &acp2.MessageMCPRequest{ConnectionID: "c1", Method: "tools/list"})
	if err != nil {
		t.Fatalf("MessageMCP: %v", err)
	}
	if string(*result) != `{"echo":"tools/list"}` {
		t.Errorf("mcp result = %s", *result)
	}

	if err := conn.NotifyMCP(ctx, &acp2.MessageMCPNotification{ConnectionID: "c1", Method: "notifications/initialized"}); err != nil {
		t.Fatalf("NotifyMCP: %v", err)
	}
	select {
	case method := <-agent.mcpNotified:
		if method != "notifications/initialized" {
			t.Errorf("notified method = %q", method)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("agent never saw the MCP notification")
	}
}

func TestSessionManagerServesLifecycleMethods(t *testing.T) {
	conn, _ := connect(t, newTestAgent(), newTestClient())
	ctx := t.Context()

	created, err := conn.NewSession(ctx, &acp2.NewSessionRequest{Cwd: "/tmp"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if _, err := conn.NewSession(ctx, &acp2.NewSessionRequest{Cwd: "/elsewhere"}); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	listed, err := conn.ListSessions(ctx, &acp2.ListSessionsRequest{Cwd: new(acp2.AbsolutePath("/tmp"))})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(listed.Sessions) != 1 || listed.Sessions[0].SessionID != created.SessionID || listed.Sessions[0].Cwd != "/tmp" {
		t.Fatalf("listed sessions in /tmp = %#v", listed.Sessions)
	}
	// Closing ends the active session but keeps it resumable.
	if _, err := conn.CloseSession(ctx, &acp2.CloseSessionRequest{SessionID: created.SessionID}); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	if _, err := conn.ResumeSession(ctx, &acp2.ResumeSessionRequest{SessionID: created.SessionID, Cwd: "/tmp"}); err != nil {
		t.Fatalf("ResumeSession after close: %v", err)
	}
	if _, err := conn.DeleteSession(ctx, &acp2.DeleteSessionRequest{SessionID: created.SessionID}); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := conn.ResumeSession(ctx, &acp2.ResumeSessionRequest{SessionID: created.SessionID, Cwd: "/tmp"}); !acp.IsCode(err, acp.ErrorCodeResourceNotFound) {
		t.Errorf("ResumeSession after delete: %v, want a resource-not-found error", err)
	}
	// Deleting an unknown session succeeds.
	if _, err := conn.DeleteSession(ctx, &acp2.DeleteSessionRequest{SessionID: created.SessionID}); err != nil {
		t.Errorf("second delete: %v, want success", err)
	}
}

func TestUnimplementedOptionalMethodIsMethodNotFound(t *testing.T) {
	conn, _ := connect(t, newTestAgent(), newTestClient())
	if _, err := conn.Login(t.Context(), &acp2.LoginAuthRequest{MethodID: "oauth"}); !acp.IsCode(err, acp.ErrorCodeMethodNotFound) {
		t.Errorf("Login error = %v, want method not found", err)
	}
	_, agentConn := connect(t, newTestAgent(), newTestClient())
	if _, err := agentConn.ConnectMCP(t.Context(), &acp2.ConnectMCPRequest{ServerID: "s1"}); !acp.IsCode(err, acp.ErrorCodeMethodNotFound) {
		t.Errorf("ConnectMCP error = %v, want method not found", err)
	}
}

func TestInvalidParamsAreRejectedBeforeTheHandler(t *testing.T) {
	conn, _ := connect(t, newTestAgent(), newTestClient())
	_, err := conn.ExtMethod(t.Context(), schema.AgentMethodsSessionPrompt, map[string]any{"prompt": []any{}})
	if !acp.IsCode(err, acp.ErrorCodeInvalidParams) {
		t.Errorf("error = %v, want invalid params", err)
	}
}

func TestCancelSessionReachesTheAgent(t *testing.T) {
	agent := newTestAgent()
	conn, _ := connect(t, agent, newTestClient())
	if err := conn.CancelSession(t.Context(), &acp2.CancelSessionNotification{SessionID: "session_1"}); err != nil {
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
	_, agentConn := connect(t, newTestAgent(), newTestClient())
	granted, err := agentConn.RequestPermission(t.Context(), &acp2.RequestPermissionRequest{
		SessionID: "session_1",
		Title:     "Edit file",
		Options:   []schema.PermissionOption{{OptionID: "allow", Name: "Allow", Kind: schema.PermissionOptionKindAllowOnce}},
	})
	if err != nil {
		t.Fatalf("RequestPermission: %v", err)
	}
	if selected, ok := granted.Outcome.Variant().(schema.RequestPermissionOutcomeSelected); !ok || selected.OptionID != "allow" {
		t.Errorf("outcome = %#v", granted.Outcome.Variant())
	}
}
