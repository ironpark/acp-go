package acp1_test

import (
	"context"
	"testing"

	"github.com/ironpark/acp-go/acp1"
)

// bareAgent implements only the required methods.
type bareAgent struct {
	*acp1.SessionManager[struct{}]
}

func (bareAgent) Initialize(context.Context, *acp1.InitializeRequest) (*acp1.InitializeResponse, error) {
	return nil, nil
}
func (bareAgent) Prompt(context.Context, *acp1.PromptRequest) (*acp1.PromptResponse, error) {
	return nil, nil
}
func (bareAgent) CancelSession(context.Context, *acp1.CancelNotification) error { return nil }

// forkingAgent adds one optional interface on top of the manager's three.
type forkingAgent struct{ bareAgent }

func (forkingAgent) ForkSession(context.Context, *acp1.ForkSessionRequest) (*acp1.ForkSessionResponse, error) {
	return nil, nil
}

type logoutAgent struct{ bareAgent }

type mcpAgent struct{ bareAgent }

func (mcpAgent) NotifyMCP(context.Context, *acp1.MessageMCPNotification) error { return nil }

func (logoutAgent) Logout(context.Context, *acp1.LogoutRequest) (*acp1.LogoutResponse, error) {
	return nil, nil
}

func TestCapabilitiesOfFollowsImplementedInterfaces(t *testing.T) {
	manager := acp1.NewSessionManager(acp1.NewMemoryStore[struct{}](), nil)

	// The manager supplies session/delete, session/resume and session/close,
	// but not session/load or session/list, which need the agent.
	caps := acp1.CapabilitiesOf(bareAgent{manager})
	if caps.LoadSession != nil {
		t.Errorf("loadSession = %v, want unset", *caps.LoadSession)
	}
	session := caps.SessionCapabilities
	if session == nil || session.Delete == nil || session.Resume == nil || session.Close == nil || session.List != nil {
		t.Errorf("session capabilities = %+v, want delete, resume and close", session)
	}
	if caps.SessionCapabilities.Fork != nil || caps.Providers != nil || caps.Nes != nil || caps.Auth != nil || caps.MCPCapabilities != nil {
		t.Errorf("advertised unimplemented capabilities: %+v", caps)
	}
	if caps := acp1.CapabilitiesOf(logoutAgent{bareAgent{manager}}); caps.Auth == nil || caps.Auth.Logout == nil {
		t.Error("auth.logout not advertised for an agent implementing LogoutHandler")
	}

	if caps := acp1.CapabilitiesOf(forkingAgent{bareAgent{manager}}); caps.SessionCapabilities.Fork == nil {
		t.Error("fork not advertised for an agent implementing SessionForker")
	}
	if !acp1.CapabilitiesOf(mcpAgent{bareAgent{manager}}).GetMCPCapabilities().GetACP() {
		t.Error("mcpCapabilities.acp not advertised for an agent implementing MCPMessageHandler")
	}
}

type readOnlyClient struct{}

func (readOnlyClient) SessionUpdate(context.Context, *acp1.SessionNotification) error { return nil }
func (readOnlyClient) RequestPermission(context.Context, *acp1.RequestPermissionRequest) (*acp1.RequestPermissionResponse, error) {
	return nil, nil
}
func (readOnlyClient) ReadTextFile(context.Context, *acp1.ReadTextFileRequest) (*acp1.ReadTextFileResponse, error) {
	return nil, nil
}

func TestClientCapabilitiesOfFollowsImplementedInterfaces(t *testing.T) {
	caps := acp1.ClientCapabilitiesOf(readOnlyClient{})
	if !caps.GetFS().GetReadTextFile() {
		t.Errorf("fs.readTextFile = %+v, want true", caps.FS)
	}
	if caps.FS.WriteTextFile == nil || *caps.FS.WriteTextFile {
		t.Errorf("fs.writeTextFile = %+v, want explicit false", caps.FS)
	}
	if caps.Terminal != nil || caps.Elicitation != nil {
		t.Errorf("advertised unimplemented capabilities: %+v", caps)
	}
}
