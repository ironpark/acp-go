package acp2_test

import (
	"context"
	"testing"

	"github.com/ironpark/acp-go/acp2"
)

type bareAgent struct {
	*acp2.SessionManager[bareSession]
}

func (bareAgent) Initialize(context.Context, *acp2.InitializeRequest) (*acp2.InitializeResponse, error) {
	return nil, nil
}
func (bareAgent) Prompt(context.Context, *acp2.PromptRequest) (*acp2.PromptResponse, error) {
	return nil, nil
}
func (bareAgent) CancelSession(context.Context, *acp2.CancelSessionNotification) error { return nil }

type authAgent struct{ bareAgent }

func (authAgent) Login(context.Context, *acp2.LoginAuthRequest) (*acp2.LoginAuthResponse, error) {
	return nil, nil
}
func (authAgent) Logout(context.Context, *acp2.LogoutAuthRequest) (*acp2.LogoutAuthResponse, error) {
	return nil, nil
}

func TestCapabilitiesOfFollowsImplementedInterfaces(t *testing.T) {
	manager := acp2.NewSessionManager(acp2.NewMemoryStore[bareSession](), nil)

	caps := acp2.CapabilitiesOf(bareAgent{manager})
	if caps.Session == nil || caps.Session.Delete == nil {
		t.Errorf("session capabilities = %+v, want delete", caps.Session)
	}
	if caps.Auth != nil || caps.Providers != nil || caps.Session.MCP != nil {
		t.Errorf("advertised unimplemented capabilities: %+v", caps)
	}
	// authMethods, not a capability, advertises auth/login and auth/logout.
	if caps := acp2.CapabilitiesOf(authAgent{bareAgent{manager}}); caps.Auth != nil {
		t.Errorf("auth = %+v for an agent implementing only AuthHandler, want nil", caps.Auth)
	}
	if caps := acp2.CapabilitiesOf(authStatusAgent{bareAgent{manager}}); caps.GetAuth().GetStatus() == nil {
		t.Error("auth.status not advertised for an agent implementing AuthStatusReader")
	}
	caps = acp2.CapabilitiesOf(sessionExtrasAgent{bareAgent{manager}})
	if caps.Session.Archive == nil || caps.Session.Status == nil || caps.Session.Title == nil {
		t.Errorf("session capabilities = %+v, want archive, status and title", caps.Session)
	}
	if caps := acp2.CapabilitiesOf(mcpAgent{bareAgent{manager}}); caps.GetSession().GetMCP().GetACP() == nil {
		t.Error("session.mcp.acp not advertised for an agent implementing MCPMessageHandler")
	}
}

type mcpAgent struct{ bareAgent }

func (mcpAgent) NotifyMCP(context.Context, *acp2.MessageMCPNotification) error { return nil }

type authStatusAgent struct{ bareAgent }

func (authStatusAgent) GetAuthStatus(context.Context, *acp2.AuthStatusRequest) (*acp2.AuthStatusResponse, error) {
	return nil, nil
}

type sessionExtrasAgent struct{ bareAgent }

func (sessionExtrasAgent) ArchiveSession(context.Context, *acp2.ArchiveSessionRequest) (*acp2.ArchiveSessionResponse, error) {
	return nil, nil
}
func (sessionExtrasAgent) UnarchiveSession(context.Context, *acp2.UnarchiveSessionRequest) (*acp2.UnarchiveSessionResponse, error) {
	return nil, nil
}
func (sessionExtrasAgent) GetSessionStatus(context.Context, *acp2.SessionStatusRequest) (*acp2.SessionStatusResponse, error) {
	return nil, nil
}
func (sessionExtrasAgent) SetSessionTitle(context.Context, *acp2.SetSessionTitleRequest) (*acp2.SetSessionTitleResponse, error) {
	return nil, nil
}
