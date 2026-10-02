package acp1_test

import (
	"context"
	"testing"
	"time"

	acp "github.com/ironpark/acp-go"
	"github.com/ironpark/acp-go/acp1"
	"github.com/ironpark/acp-go/acp1/acp1test"
)

// slowAgent's turn asks for permission, then works for delay or until the
// turn is cancelled.
type slowAgent struct {
	*acp1.SessionManager[struct{}]
	client acp1.Client
	delay  time.Duration
}

func newSlowAgent(delay time.Duration) *slowAgent {
	return &slowAgent{delay: delay, SessionManager: acp1.NewSessionManager(acp1.NewMemoryStore[struct{}](),
		func(context.Context, *acp1.NewSessionRequest) (acp1.SessionID, struct{}, error) {
			return acp1.GenerateSessionID(), struct{}{}, nil
		})}
}

func (*slowAgent) Initialize(context.Context, *acp1.InitializeRequest) (*acp1.InitializeResponse, error) {
	return &acp1.InitializeResponse{ProtocolVersion: acp1.ProtocolVersion}, nil
}

func (a *slowAgent) Prompt(ctx context.Context, params *acp1.PromptRequest) (*acp1.PromptResponse, error) {
	return a.RunTurn(ctx, params.SessionID, func(ctx context.Context, _ struct{}) (acp1.StopReason, error) {
		stream := acp1.NewSessionStream(a.client, params.SessionID)
		if _, _, err := stream.RequestPermission(ctx, acp1.ToolCallUpdate{ToolCallID: "t1"}); err != nil {
			return "", err
		}
		select {
		case <-time.After(a.delay):
			return acp1.StopReasonEndTurn, nil
		case <-ctx.Done():
			return acp1.StopReasonCancelled, nil
		}
	})
}

// slowClient answers permission requests after delay.
type slowClient struct {
	acp1test.Client
	delay time.Duration
}

func (c *slowClient) RequestPermission(_ context.Context, params *acp1.RequestPermissionRequest) (*acp1.RequestPermissionResponse, error) {
	time.Sleep(c.delay)
	return acp1test.AllowOnce(params), nil
}

func connectSlow(t *testing.T, agent *slowAgent, client acp1.Client, opts ...acp.Option) *acp1.ClientSession {
	t.Helper()
	_, conn := acp1.Pipe(t.Context(), func(c *acp1.AgentSideConnection) acp1.Agent {
		agent.client = c
		return agent
	}, func(*acp1.ClientSideConnection) acp1.Client { return client }, opts...)
	session, err := conn.StartSession(t.Context(), &acp1.NewSessionRequest{Cwd: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

// TestPromptContextCancelsTheTurn cancels the context a prompt was sent
// with: the client sends session/cancel and the turn ends cancelled, with
// the agent's answer, instead of being abandoned.
func TestPromptContextCancelsTheTurn(t *testing.T) {
	session := connectSlow(t, newSlowAgent(time.Minute), &slowClient{})
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	turn, err := session.Prompt(ctx, acp1.TextBlock("work"))
	if err != nil {
		t.Fatal(err)
	}
	response, err := turn.Wait()
	if err != nil || response.StopReason != acp1.StopReasonCancelled {
		t.Fatalf("turn ended with %+v %v, want the cancelled stop reason", response, err)
	}
	// The agent has ended the turn too, so the session takes a new prompt.
	next, err := session.Prompt(t.Context(), acp1.TextBlock("again"))
	if err != nil {
		t.Fatalf("next prompt: %v", err)
	}
	go func() { _ = session.Cancel(t.Context()) }()
	_, _ = next.Wait()
}

// TestRequestTimeoutSparesUserPacedRequests sets a request timeout shorter
// than the user takes to answer and the turn takes to run: neither the
// permission request nor the prompt is cut short.
func TestRequestTimeoutSparesUserPacedRequests(t *testing.T) {
	session := connectSlow(t, newSlowAgent(150*time.Millisecond), &slowClient{delay: 150 * time.Millisecond},
		acp.WithRequestTimeout(50*time.Millisecond))
	turn, err := session.Prompt(t.Context(), acp1.TextBlock("work"))
	if err != nil {
		t.Fatal(err)
	}
	response, err := turn.Wait()
	if err != nil || response.StopReason != acp1.StopReasonEndTurn {
		t.Fatalf("turn ended with %+v %v, want end_turn", response, err)
	}
}
