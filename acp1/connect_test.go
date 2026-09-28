package acp1_test

import (
	"context"
	"io"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	acp "github.com/ironpark/acp-go"
	"github.com/ironpark/acp-go/acp1"
	"github.com/ironpark/acp-go/acphttp"
)

// permissionAgent asks the client for permission mid-turn, a request that
// travels on the session stream and is answered by a POST.
type permissionAgent struct{ *testAgent }

func (a *permissionAgent) Prompt(ctx context.Context, params *acp1.PromptRequest) (*acp1.PromptResponse, error) {
	permission, err := a.client.RequestPermission(ctx, &acp1.RequestPermissionRequest{
		SessionID: params.SessionID,
		ToolCall:  acp1.ToolCallUpdate{ToolCallID: "call_1"},
		Options:   []acp1.PermissionOption{{OptionID: "allow", Name: "Allow", Kind: acp1.PermissionOptionKindAllowOnce}},
	})
	if err != nil {
		return nil, err
	}
	text := "denied"
	if selected, ok := permission.Outcome.As[acp1.RequestPermissionOutcomeSelected](); ok {
		text = "allowed " + string(selected.OptionID)
	}
	if err := acp1.NewSessionStream(a.client, params.SessionID).SendText(ctx, text); err != nil {
		return nil, err
	}
	return &acp1.PromptResponse{StopReason: acp1.StopReasonEndTurn}, nil
}

// agentServer serves a new agent from newAgent on each HTTP connection.
func agentServer(newAgent func(*acp1.AgentSideConnection) acp1.Agent) *acphttp.Server {
	return acphttp.NewServer(func(ctx context.Context, tr acp.Transport) error {
		return acp1.NewAgentSideConnection(newAgent, tr).Start(ctx)
	})
}

func TestConnectAgentOverHTTP(t *testing.T) {
	server := agentServer(func(c *acp1.AgentSideConnection) acp1.Agent {
		a := &permissionAgent{newTestAgent()}
		a.client = c
		return a
	})
	ts := httptest.NewServer(server)
	defer ts.Close()
	defer server.Close()

	transports := map[string]func() acp.Transport{
		"streamable HTTP": func() acp.Transport { return acphttp.NewClientTransport(ts.URL) },
		"WebSocket": func() acp.Transport {
			ws, err := acphttp.DialWebSocket(t.Context(), "ws"+strings.TrimPrefix(ts.URL, "http"))
			if err != nil {
				t.Fatal(err)
			}
			return ws
		},
	}
	for name, dial := range transports {
		t.Run(name, func(t *testing.T) {
			agent := acp1.ConnectAgent(t.Context(), dial(), func(*acp1.ClientSideConnection) acp1.Client {
				return newTestClient()
			})
			if _, err := agent.Initialize(t.Context(), &acp1.InitializeRequest{ProtocolVersion: acp1.ProtocolVersion}); err != nil {
				t.Fatal(err)
			}
			// Two sessions on one connection.
			for range 2 {
				session, err := agent.StartSession(t.Context(), &acp1.NewSessionRequest{Cwd: "/tmp"})
				if err != nil {
					t.Fatal(err)
				}
				turn, err := session.Prompt(t.Context(), acp1.TextBlock("hi"))
				if err != nil {
					t.Fatal(err)
				}
				text, response, err := turn.Text()
				if err != nil || text != "allowed allow" || response.StopReason != acp1.StopReasonEndTurn {
					t.Fatalf("turn: %q %+v %v", text, response, err)
				}
			}
			if _, err := acp.CallExt[map[string]string](t.Context(), agent, "_test/echo", map[string]int{"n": 1}); err != nil {
				t.Fatal(err)
			}
			if err := agent.Close(); err != nil {
				t.Fatal(err)
			}
			if err := agent.Wait(); err != nil {
				t.Fatalf("Wait after Close = %v", err)
			}
		})
	}
}

// TestConnectAgentOverSocketCloses: over a socket whose peer stays open and
// never writes, Close must still return; a stdio transport's read ignores
// cancellation, so the transport has to be closed to end it (#11).
func TestConnectAgentOverSocketCloses(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	go func() { _, _ = io.Copy(io.Discard, b) }() // the agent's end stays open
	agent := acp1.ConnectAgent(t.Context(), acp.NewStdioTransport(a, a), func(*acp1.ClientSideConnection) acp1.Client {
		return newTestClient()
	})
	time.Sleep(100 * time.Millisecond) // let the read loop block

	closed := make(chan error, 1)
	go func() { closed <- agent.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close blocked on a read the connection could not cancel")
	}
	if err := agent.Wait(); err != nil {
		t.Fatalf("Wait after Close = %v", err)
	}
}
