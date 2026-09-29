package acp1_test

import (
	"context"
	"errors"
	"testing"
	"time"

	acp "github.com/ironpark/acp-go"
	"github.com/ironpark/acp-go/acp1"
	schema "github.com/ironpark/acp-go/schema/v1"
)

// streamingAgent sends several chunks per turn and blocks the turn until
// release is closed, so tests can observe a turn in progress.
type streamingAgent struct {
	*testAgent
	release chan struct{}
}

func (a *streamingAgent) Prompt(ctx context.Context, params *acp1.PromptRequest) (*acp1.PromptResponse, error) {
	stream := acp1.NewSessionStream(a.client, params.SessionID)
	for _, part := range []string{"Hel", "lo, ", "world"} {
		if err := stream.SendText(ctx, part); err != nil {
			return nil, err
		}
	}
	if err := stream.StartToolCall(ctx, "call_1", "Read", schema.ToolKindRead); err != nil {
		return nil, err
	}
	<-a.release
	return &acp1.PromptResponse{StopReason: schema.StopReasonEndTurn}, nil
}

func TestTurnCollectsItsUpdates(t *testing.T) {
	agent := &streamingAgent{testAgent: newTestAgent(), release: make(chan struct{})}
	client := newTestClient()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, conn := acp1.Pipe(ctx, func(c *acp1.AgentSideConnection) acp1.Agent {
		agent.client = c
		return agent
	}, func(*acp1.ClientSideConnection) acp1.Client { return client })

	session, err := conn.StartSession(ctx, &acp1.NewSessionRequest{Cwd: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := session.Prompt(ctx, acp1.TextBlock("hi"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Prompt(ctx, acp1.TextBlock("again")); !errors.Is(err, acp.ErrTurnInProgress) {
		t.Fatalf("second prompt during a turn: %v", err)
	}
	close(agent.release)

	var tags []string
	for update := range turn.Updates() {
		tags = append(tags, update.Tag())
	}
	want := []string{"agent_message_chunk", "agent_message_chunk", "agent_message_chunk", "tool_call"}
	if len(tags) != len(want) {
		t.Fatalf("got updates %v, want %v", tags, want)
	}
	response, err := turn.Wait()
	if err != nil || response.StopReason != schema.StopReasonEndTurn {
		t.Fatalf("got %+v %v", response, err)
	}
	if len(client.updates) != len(want) {
		t.Fatalf("client saw %d updates, want %d", len(client.updates), len(want))
	}

	// The session is free again, and Text gathers the message chunks.
	agent.release = make(chan struct{})
	close(agent.release)
	turn, err = session.Prompt(ctx, acp1.TextBlock("hi"))
	if err != nil {
		t.Fatal(err)
	}
	if text, _, err := turn.Text(); err != nil || text != "Hello, world" {
		t.Fatalf("got %q %v", text, err)
	}
}

// cancellableAgent relies on the embedded manager's Cancel to stop its turns.
type cancellableAgent struct {
	*acp1.SessionManager[struct{}]
	started chan struct{}
}

func (cancellableAgent) Initialize(context.Context, *acp1.InitializeRequest) (*acp1.InitializeResponse, error) {
	return &acp1.InitializeResponse{ProtocolVersion: acp1.ProtocolVersion}, nil
}

// Prompt runs until cancelled and then fails: RunTurn still answers the
// cancelled turn with the cancelled stop reason.
func (a cancellableAgent) Prompt(ctx context.Context, params *acp1.PromptRequest) (*acp1.PromptResponse, error) {
	return a.RunTurn(ctx, params.SessionID, func(ctx context.Context, _ struct{}) (acp1.StopReason, error) {
		close(a.started)
		<-ctx.Done()
		return "", ctx.Err()
	})
}

func TestSessionManagerCancelStopsTheTurn(t *testing.T) {
	agent := cancellableAgent{
		SessionManager: acp1.NewSessionManager(acp1.NewMemoryStore[struct{}](),
			func(context.Context, *acp1.NewSessionRequest) (acp1.SessionID, struct{}, error) {
				return acp1.GenerateSessionID(), struct{}{}, nil
			}),
		started: make(chan struct{}),
	}
	_, conn := acp1.Pipe(t.Context(), func(*acp1.AgentSideConnection) acp1.Agent { return agent },
		func(*acp1.ClientSideConnection) acp1.Client { return newTestClient() })

	session, err := conn.StartSession(t.Context(), &acp1.NewSessionRequest{Cwd: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := session.Prompt(t.Context(), acp1.TextBlock("work"))
	if err != nil {
		t.Fatal(err)
	}
	<-agent.started
	// A v1 client that ignores the one-turn rule gets an invalid-request error
	// from RunTurn, and the running turn is left alone. Bypass ClientSession,
	// which would refuse locally.
	_, err = conn.Prompt(t.Context(), &acp1.PromptRequest{SessionID: session.ID, Prompt: []acp1.ContentBlock{acp1.TextBlock("again")}})
	if !errors.Is(err, acp.ErrTurnInProgress) {
		t.Fatalf("overlapping prompt: %v, want acp.ErrTurnInProgress from across the wire", err)
	}
	if err := session.Cancel(t.Context()); err != nil {
		t.Fatal(err)
	}
	response, err := turn.Wait()
	if err != nil || response.StopReason != schema.StopReasonCancelled {
		t.Fatalf("got %+v %v", response, err)
	}
}

// slowStartAgent does a little work, as a store lookup would, before it
// starts the turn, then waits briefly for a cancel.
type slowStartAgent struct {
	*acp1.SessionManager[struct{}]
}

func (slowStartAgent) Initialize(context.Context, *acp1.InitializeRequest) (*acp1.InitializeResponse, error) {
	return &acp1.InitializeResponse{ProtocolVersion: acp1.ProtocolVersion}, nil
}

func (a slowStartAgent) Prompt(ctx context.Context, params *acp1.PromptRequest) (*acp1.PromptResponse, error) {
	time.Sleep(time.Millisecond)
	ctx, done, err := a.BeginTurn(ctx, params.SessionID)
	if err != nil {
		return nil, err
	}
	defer done()
	select {
	case <-ctx.Done():
		if acp.TurnCancelled(ctx) {
			return &acp1.PromptResponse{StopReason: schema.StopReasonCancelled}, nil
		}
		return nil, ctx.Err()
	case <-time.After(100 * time.Millisecond):
		return &acp1.PromptResponse{StopReason: schema.StopReasonEndTurn}, nil
	}
}

// TestCancelRightAfterPromptCancelsTheTurn sends session/cancel as soon as
// Prompt returns, before the agent has started the turn. The cancel follows
// the prompt, so it must still end that turn as cancelled, while a cancel
// sent with no prompt outstanding must not affect the next turn.
func TestCancelRightAfterPromptCancelsTheTurn(t *testing.T) {
	agent := slowStartAgent{acp1.NewSessionManager(acp1.NewMemoryStore[struct{}](),
		func(context.Context, *acp1.NewSessionRequest) (acp1.SessionID, struct{}, error) {
			return acp1.GenerateSessionID(), struct{}{}, nil
		})}
	_, conn := acp1.Pipe(t.Context(), func(*acp1.AgentSideConnection) acp1.Agent { return agent },
		func(*acp1.ClientSideConnection) acp1.Client { return newTestClient() })
	session, err := conn.StartSession(t.Context(), &acp1.NewSessionRequest{Cwd: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}

	for i := range 20 {
		turn, err := session.Prompt(t.Context(), acp1.TextBlock("work"))
		if err != nil {
			t.Fatal(err)
		}
		if err := session.Cancel(t.Context()); err != nil {
			t.Fatal(err)
		}
		response, err := turn.Wait()
		if err != nil || response.StopReason != schema.StopReasonCancelled {
			t.Fatalf("turn %d: got %+v %v, want cancelled", i, response, err)
		}
	}

	if err := session.Cancel(t.Context()); err != nil {
		t.Fatal(err)
	}
	turn, err := session.Prompt(t.Context(), acp1.TextBlock("work"))
	if err != nil {
		t.Fatal(err)
	}
	if response, err := turn.Wait(); err != nil || response.StopReason != schema.StopReasonEndTurn {
		t.Fatalf("turn after a stray cancel: got %+v %v, want end_turn", response, err)
	}
}
