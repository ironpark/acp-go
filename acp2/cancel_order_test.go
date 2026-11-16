package acp2_test

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ironpark/acp-go/acp2"
	"github.com/ironpark/acp-go/acp2/acp2test"
)

// lingeringAgent's first piece of work runs until it is cancelled and then
// takes a moment to wind down, as work that has to stop a process would;
// later work ends at once. It counts the pieces of work running together.
type lingeringAgent struct {
	*acp2.SessionManager[bareSession]
	client  acp2.Client
	started chan struct{} // closed once the first work runs
	works   atomic.Int32
	running atomic.Int32
	overlap atomic.Bool // two pieces of work ran at once
}

func (*lingeringAgent) Initialize(context.Context, *acp2.InitializeRequest) (*acp2.InitializeResponse, error) {
	return &acp2.InitializeResponse{ProtocolVersion: acp2.ProtocolVersion}, nil
}

func (a *lingeringAgent) Prompt(ctx context.Context, params *acp2.PromptRequest) (*acp2.PromptResponse, error) {
	stream := acp2.NewSessionStream(a.client, params.SessionID)
	if _, err := a.StartTurn(ctx, params.SessionID, stream, a.work); err != nil {
		return nil, err
	}
	return &acp2.PromptResponse{MessageID: acp2.GenerateMessageID()}, nil
}

func (a *lingeringAgent) work(ctx context.Context, _ bareSession) (acp2.StopReason, error) {
	if a.running.Add(1) > 1 {
		a.overlap.Store(true)
	}
	defer a.running.Add(-1)
	if a.works.Add(1) > 1 {
		return acp2.StopReasonEndTurn, nil
	}
	close(a.started)
	<-ctx.Done()
	time.Sleep(50 * time.Millisecond) // winding down
	return acp2.StopReasonEndTurn, nil
}

// A prompt sent right after session/cancel starts new foreground work. That
// work must not begin, nor be reported running, until the cancelled work has
// ended and reported idle: otherwise the client reads the old work's idle as
// the end of the new work, and the agent runs both on one session.
func TestPromptAfterCancelWaitsForTheCancelledWork(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	agent := &lingeringAgent{
		SessionManager: acp2.NewSessionManager(acp2.NewMemoryStore[bareSession](),
			func(context.Context, *acp2.NewSessionRequest) (acp2.SessionID, bareSession, error) {
				return acp2.GenerateSessionID(), bareSession{}, nil
			}),
		started: make(chan struct{}),
	}
	client := &acp2test.Client{}
	conn := acp2test.Connect(t, func(c *acp2.AgentSideConnection) acp2.Agent {
		agent.client = c
		return agent
	}, client)
	session, err := conn.StartSession(ctx, &acp2.NewSessionRequest{Cwd: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := session.Prompt(ctx, acp2.TextBlock("first")); err != nil {
		t.Fatal(err)
	}
	<-agent.started
	if err := session.Cancel(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Prompt(ctx, acp2.TextBlock("second")); err != nil {
		t.Fatal(err)
	}
	for i, want := range []acp2.StopReason{acp2.StopReasonCancelled, acp2.StopReasonEndTurn} {
		if reason, err := session.UntilIdle(ctx).Wait(); err != nil || reason != want {
			t.Fatalf("turn %d ended %q, %v; want %q", i+1, reason, err, want)
		}
	}

	var states []string
	for _, n := range client.Updates() {
		if state, ok := n.Update.As[acp2.SessionUpdateStateUpdate](); ok {
			states = append(states, state.Value.Tag())
		}
	}
	if want := []string{"running", "idle", "running", "idle"}; !slices.Equal(states, want) {
		t.Errorf("state updates = %v, want %v", states, want)
	}
	if agent.overlap.Load() {
		t.Error("the new work started while the cancelled work was still winding down")
	}
}
