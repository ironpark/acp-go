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

// extrasAgent serves the methods added in schema v2.0.0-alpha.13 on top of
// the helper agent; tool_call/cancel and terminal/cancel come from the
// embedded SessionManager.
type extrasAgent struct {
	helperAgent
	archived chan acp2.SessionID
}

func (a *extrasAgent) SetSessionTitle(_ context.Context, params *acp2.SetSessionTitleRequest) (*acp2.SetSessionTitleResponse, error) {
	return &acp2.SetSessionTitleResponse{Title: "[" + params.Title + "]"}, nil
}

func (a *extrasAgent) ArchiveSession(_ context.Context, params *acp2.ArchiveSessionRequest) (*acp2.ArchiveSessionResponse, error) {
	a.archived <- params.SessionID
	return &acp2.ArchiveSessionResponse{}, nil
}

func (a *extrasAgent) UnarchiveSession(context.Context, *acp2.UnarchiveSessionRequest) (*acp2.UnarchiveSessionResponse, error) {
	return &acp2.UnarchiveSessionResponse{}, nil
}

func (a *extrasAgent) GetSessionStatus(ctx context.Context, params *acp2.SessionStatusRequest) (*acp2.SessionStatusResponse, error) {
	if _, err := a.Lookup(ctx, params.SessionID); err != nil {
		return nil, err
	}
	return &acp2.SessionStatusResponse{Status: acp2.SessionStatusOpen}, nil
}

func (a *extrasAgent) GetAuthStatus(context.Context, *acp2.AuthStatusRequest) (*acp2.AuthStatusResponse, error) {
	return &acp2.AuthStatusResponse{State: acp2.AuthStateAuthenticated, Accounts: []acp2.AuthAccount{{Label: "API key"}}}, nil
}

// authStatusClient records auth/status_update notifications.
type authStatusClient struct {
	acp2test.Client
	updates chan acp2.AuthState
}

func (c *authStatusClient) AuthStatusUpdate(_ context.Context, params *acp2.AuthStatusUpdateNotification) error {
	c.updates <- params.State
	return nil
}

func connectExtras(t *testing.T, client acp2.Client) (*extrasAgent, *acp2.ClientSideConnection, *acp2.AgentSideConnection) {
	t.Helper()
	agent := &extrasAgent{archived: make(chan acp2.SessionID, 1)}
	agent.SessionManager = acp2.NewSessionManager(acp2.NewMemoryStore[commandSession](),
		func(context.Context, *acp2.NewSessionRequest) (acp2.SessionID, commandSession, error) {
			return acp2.GenerateSessionID(), commandSession{}, nil
		})
	var agentConn *acp2.AgentSideConnection
	conn := acp2test.Connect(t, func(c *acp2.AgentSideConnection) acp2.Agent {
		agentConn, agent.client = c, c
		return agent
	}, client)
	return agent, conn, agentConn
}

func testContext(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestToolCallCancelThroughSessionManager(t *testing.T) {
	ctx := testContext(t)
	client := &acp2test.Client{}
	agent, conn, agentConn := connectExtras(t, client)
	session, err := conn.StartSession(ctx, &acp2.NewSessionRequest{Cwd: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}

	// The agent runs a cancellable tool call until the client cancels it.
	stream := acp2.NewSessionStream(agentConn, session.ID)
	callCtx, done := agent.TrackToolCall(ctx, session.ID, "call_1")
	if err := stream.StartToolCall(callCtx, "call_1", "Run", acp2.ToolKindExecute, acp2.WithCancellable(true)); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		defer done()
		<-callCtx.Done()
		if !acp.WorkCancelled(callCtx) {
			finished <- context.Cause(callCtx)
			return
		}
		finished <- stream.UpdateToolCallStatus(context.WithoutCancel(callCtx), "call_1", acp2.ToolCallStatusCancelled)
	}()

	update, err := client.WaitFor(ctx, func(n *acp2.UpdateSessionNotification) bool { return n.Update.Tag() == "tool_call_update" })
	if err != nil {
		t.Fatal(err)
	}
	call, _ := update.Update.As[acp2.SessionUpdateToolCallUpdate]()
	if got, ok := call.Capabilities.Get(); !ok || got.Cancel == nil {
		t.Errorf("tool call capabilities = %+v, want cancel", call.Capabilities)
	}

	if err := session.CancelToolCall(ctx, "call_1"); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatalf("tool call did not end cancelled: %v", err)
	}
	if _, err := client.WaitFor(ctx, func(n *acp2.UpdateSessionNotification) bool {
		u, ok := n.Update.As[acp2.SessionUpdateToolCallUpdate]()
		status, _ := u.Status.Get()
		return ok && status == acp2.ToolCallStatusCancelled
	}); err != nil {
		t.Fatal(err)
	}

	// Once final, or never tracked, the protocol answers invalid params.
	if err := session.CancelToolCall(ctx, "call_1"); !acp.IsCode(err, acp.ErrorCodeInvalidParams) {
		t.Errorf("cancel after the end: got %v, want invalid params", err)
	}
	if err := session.CancelTerminal(ctx, "term_1"); !acp.IsCode(err, acp.ErrorCodeInvalidParams) {
		t.Errorf("cancel of an untracked terminal: got %v, want invalid params", err)
	}
}

func TestClientSessionExtras(t *testing.T) {
	ctx := testContext(t)
	agent, conn, _ := connectExtras(t, &acp2test.Client{})

	caps := acp2.CapabilitiesOf(agent)
	if caps.GetSession().GetTitle() == nil || caps.GetSession().GetArchive() == nil ||
		caps.GetSession().GetStatus() == nil || caps.GetAuth().GetStatus() == nil {
		t.Errorf("capabilities = %+v, want session.title, .archive, .status and auth.status", caps)
	}

	session, err := conn.StartSession(ctx, &acp2.NewSessionRequest{Cwd: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	if title, err := session.SetTitle(ctx, "plan"); err != nil || title != "[plan]" {
		t.Errorf("SetTitle = %q, %v", title, err)
	}
	if status, err := session.Status(ctx); err != nil || status.Status != acp2.SessionStatusOpen {
		t.Errorf("Status = %+v, %v", status, err)
	}

	if err := session.Archive(ctx); err != nil {
		t.Fatal(err)
	}
	if id := <-agent.archived; id != session.ID {
		t.Errorf("archived %q, want %q", id, session.ID)
	}
	// Archiving closes the session, so the handle stops queueing updates.
	if _, err := session.NextUpdate(ctx); err == nil {
		t.Error("NextUpdate after Archive returned an update")
	}
	if err := session.Unarchive(ctx); err != nil {
		t.Fatal(err)
	}

	// An unknown session carries its id as error data.
	_, err = conn.Session("session_missing").Status(ctx)
	if id, ok := acp.SessionNotFoundID(err); !ok || id != "session_missing" {
		t.Errorf("unknown session: got %v (id %q, %v)", err, id, ok)
	}
}

func TestAuthStatusIsRecorded(t *testing.T) {
	ctx := testContext(t)

	// A client without AuthStatusListener still has the status recorded.
	_, conn, agentConn := connectExtras(t, &acp2test.Client{})
	if conn.AuthStatus() != nil {
		t.Fatal("auth status known before any report")
	}
	if _, err := conn.GetAuthStatus(ctx, &acp2.AuthStatusRequest{}); err != nil {
		t.Fatal(err)
	}
	if got := conn.AuthStatus(); got == nil || got.State != acp2.AuthStateAuthenticated || len(got.Accounts) != 1 {
		t.Errorf("after auth/status: %+v", got)
	}
	if err := agentConn.AuthStatusUpdate(ctx, &acp2.AuthStatusUpdateNotification{State: acp2.AuthStateUnauthenticated}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, func() bool {
		got := conn.AuthStatus()
		return got != nil && got.State == acp2.AuthStateUnauthenticated
	})

	// A listener also receives the notification.
	client := &authStatusClient{updates: make(chan acp2.AuthState, 1)}
	_, _, agentConn = connectExtras(t, client)
	if err := agentConn.AuthStatusUpdate(ctx, &acp2.AuthStatusUpdateNotification{State: acp2.AuthStateNotRequired}); err != nil {
		t.Fatal(err)
	}
	select {
	case state := <-client.updates:
		if state != acp2.AuthStateNotRequired {
			t.Errorf("auth/status_update state = %q", state)
		}
	case <-ctx.Done():
		t.Fatal("auth/status_update not delivered")
	}
}

func TestInitializeBelowV2IsUnsupported(t *testing.T) {
	ctx := testContext(t)
	_, conn, _ := connectExtras(t, &acp2test.Client{})
	_, err := conn.Initialize(ctx, &acp2.InitializeRequest{ProtocolVersion: 1, Info: schema.Implementation{Name: "c", Version: "0"}})
	supported, ok := acp.SupportedProtocolVersions(err)
	if !ok || len(supported) != 1 || supported[0] != 2 {
		t.Fatalf("got %v (supported %v), want -32022 listing [2]", err, supported)
	}
}

// waitFor polls cond until it holds or ctx ends.
func waitFor(t *testing.T, ctx context.Context, cond func() bool) {
	t.Helper()
	for !cond() {
		select {
		case <-ctx.Done():
			t.Fatal("condition not met")
		case <-time.After(5 * time.Millisecond):
		}
	}
}
