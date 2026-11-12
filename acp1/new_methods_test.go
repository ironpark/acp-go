package acp1_test

import (
	"context"
	"testing"
	"time"

	acp "github.com/ironpark/acp-go"
	"github.com/ironpark/acp-go/acp1"
	"github.com/ironpark/acp-go/acp1/acp1test"
)

// extrasAgent serves the methods added in schema v1.29 on top of the helper
// agent.
type extrasAgent struct {
	*helperAgent
	agentConn *acp1.AgentSideConnection
}

func (a *extrasAgent) SetSessionTitle(_ context.Context, params *acp1.SetSessionTitleRequest) (*acp1.SetSessionTitleResponse, error) {
	return &acp1.SetSessionTitleResponse{Title: params.Title + "!"}, nil
}

func (a *extrasAgent) ArchiveSession(context.Context, *acp1.ArchiveSessionRequest) (*acp1.ArchiveSessionResponse, error) {
	return &acp1.ArchiveSessionResponse{}, nil
}

func (a *extrasAgent) UnarchiveSession(context.Context, *acp1.UnarchiveSessionRequest) (*acp1.UnarchiveSessionResponse, error) {
	return &acp1.UnarchiveSessionResponse{}, nil
}

func (a *extrasAgent) GetSessionStatus(ctx context.Context, params *acp1.SessionStatusRequest) (*acp1.SessionStatusResponse, error) {
	if _, err := a.Lookup(ctx, params.SessionID); err != nil {
		return nil, err
	}
	return &acp1.SessionStatusResponse{Status: acp1.SessionStatusSaved}, nil
}

func (a *extrasAgent) GetSessionOptions(ctx context.Context, params *acp1.SessionOptionsRequest) (*acp1.SessionOptionsResponse, error) {
	if params.SessionID != nil {
		if _, err := a.Lookup(ctx, *params.SessionID); err != nil {
			return nil, err
		}
	}
	return &acp1.SessionOptionsResponse{Modes: &acp1.SessionModeState{CurrentModeID: "ask"}}, nil
}

func (a *extrasAgent) GetAuthStatus(context.Context, *acp1.AuthStatusRequest) (*acp1.AuthStatusResponse, error) {
	return &acp1.AuthStatusResponse{State: acp1.AuthStateNotRequired}, nil
}

func TestClientSessionExtras(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	agent := &extrasAgent{helperAgent: newHelperAgent(acp1.NewMemoryStore[commandSession]())}
	conn := acp1test.Connect(t, func(c *acp1.AgentSideConnection) acp1.Agent {
		agent.agentConn = c
		return agent
	}, &acp1test.Client{})

	caps := acp1.CapabilitiesOf(agent)
	if caps.GetSessionCapabilities().GetOptions() == nil {
		t.Error("sessionCapabilities.options not advertised for a SessionOptionsReader")
	}
	if options, err := conn.GetSessionOptions(ctx, &acp1.SessionOptionsRequest{Cwd: "/tmp"}); err != nil || options.GetModes().CurrentModeID != "ask" {
		t.Errorf("session/options = %+v, %v", options, err)
	}
	missing := acp1.SessionID("session_missing")
	_, err := conn.GetSessionOptions(ctx, &acp1.SessionOptionsRequest{Cwd: "/tmp", SessionID: &missing})
	if id, ok := acp.SessionNotFoundID(err); !ok || id != string(missing) {
		t.Errorf("options of an unknown session: %v", err)
	}
	if content := acp1.ToolSession("session_sub"); content.Tag() != "session" {
		t.Errorf("ToolSession tag = %q", content.Tag())
	}
	if caps.GetSessionCapabilities().GetTitle() == nil || caps.GetSessionCapabilities().GetArchive() == nil ||
		caps.GetSessionCapabilities().GetStatus() == nil || caps.GetAuth().GetStatus() == nil {
		t.Errorf("capabilities = %+v", caps)
	}

	session, err := conn.StartSession(ctx, &acp1.NewSessionRequest{Cwd: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	if title, err := session.SetTitle(ctx, "plan"); err != nil || title != "plan!" {
		t.Errorf("SetTitle = %q, %v", title, err)
	}
	if status, err := session.Status(ctx); err != nil || status.Status != acp1.SessionStatusSaved {
		t.Errorf("Status = %+v, %v", status, err)
	}
	if err := session.Archive(ctx); err != nil {
		t.Fatal(err)
	}
	if err := session.Unarchive(ctx); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(ctx); err != nil {
		t.Fatal(err)
	}

	_, err = conn.Session("session_missing").Status(ctx)
	if id, ok := acp.SessionNotFoundID(err); !ok || id != "session_missing" {
		t.Errorf("unknown session: got %v (id %q, %v)", err, id, ok)
	}

	if _, err := conn.GetAuthStatus(ctx, &acp1.AuthStatusRequest{}); err != nil {
		t.Fatal(err)
	}
	if got := conn.AuthStatus(); got == nil || got.State != acp1.AuthStateNotRequired {
		t.Errorf("AuthStatus = %+v", got)
	}
	if err := agent.agentConn.AuthStatusUpdate(ctx, &acp1.AuthStatusUpdateNotification{State: acp1.AuthStateUnauthenticated}); err != nil {
		t.Fatal(err)
	}
	for {
		if got := conn.AuthStatus(); got.State == acp1.AuthStateUnauthenticated {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("auth/status_update not recorded")
		case <-time.After(5 * time.Millisecond):
		}
	}
}
