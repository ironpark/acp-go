package acp2_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	acp "github.com/ironpark/acp-go"
	"github.com/ironpark/acp-go/acp2"
	"github.com/ironpark/acp-go/acp2/acp2test"
	schema "github.com/ironpark/acp-go/schema/v2"
)

// v2Agent follows the v2 prompt lifecycle: the prompt response only accepts
// the message, and the turn ends with an idle state update. With idleFirst,
// the whole turn is reported before the response is sent.
type v2Agent struct {
	*testAgent
	idleFirst bool
}

func (a *v2Agent) Prompt(ctx context.Context, params *acp2.PromptRequest) (*acp2.PromptResponse, error) {
	stream := acp2.NewSessionStream(a.client, params.SessionID)
	work := func() {
		ctx := context.Background()
		_ = stream.Running(ctx)
		_ = stream.SendText(ctx, "m1", "draft")
		_ = stream.Send(ctx, schema.SessionUpdateAgentMessage{MessageID: "m1", Content: []acp2.ContentBlock{acp2.TextBlock("Hello")}})
		_ = stream.SendText(ctx, "m1", ", world")
		_ = stream.Idle(ctx, schema.StopReasonEndTurn)
	}
	if a.idleFirst {
		work()
	} else {
		go work()
	}
	return &acp2.PromptResponse{MessageID: "user_1"}, nil
}

func TestTurnEndsOnIdle(t *testing.T) {
	for _, idleFirst := range []bool{false, true} {
		agent := &v2Agent{testAgent: newTestAgent(), idleFirst: idleFirst}
		_, conn := acp2.Pipe(t.Context(), func(c *acp2.AgentSideConnection) acp2.Agent {
			agent.client = c
			return agent
		}, func(*acp2.ClientSideConnection) acp2.Client { return newTestClient() })

		session, err := conn.StartSession(t.Context(), &acp2.NewSessionRequest{Cwd: "/tmp"})
		if err != nil {
			t.Fatal(err)
		}
		turn, messageID, err := session.Prompt(t.Context(), acp2.TextBlock("hi"))
		if err != nil || messageID != "user_1" {
			t.Fatalf("idleFirst=%v: prompt %q %v", idleFirst, messageID, err)
		}
		text, reason, err := turn.Text()
		if err != nil || text != "Hello, world" {
			t.Fatalf("idleFirst=%v: got %q %v", idleFirst, text, err)
		}
		if reason != schema.StopReasonEndTurn {
			t.Fatalf("idleFirst=%v: got %v", idleFirst, reason)
		}
		// The next prompt starts a new turn once this one has ended.
		if next, _, err := session.Prompt(t.Context(), acp2.TextBlock("again")); err != nil {
			t.Fatal(err)
		} else if _, err := next.Wait(); err != nil {
			t.Fatal(err)
		}
	}
}

// cancellableAgent runs each turn in the background, as v2 allows, folds
// prompts that arrive mid-turn into it, and relies on the embedded manager's
// CancelSession to stop it.
type cancellableAgent struct {
	*acp2.SessionManager[bareSession]
	client  acp2.Client
	started chan struct{}
	ended   chan error // receives the cause the work's context ended with, if set
}

func (cancellableAgent) Initialize(context.Context, *acp2.InitializeRequest) (*acp2.InitializeResponse, error) {
	return &acp2.InitializeResponse{ProtocolVersion: acp2.ProtocolVersion}, nil
}

// Prompt starts work that runs until cancelled and reports it ended its
// turn: StartTurn reports the cancelled stop reason instead.
func (a *cancellableAgent) Prompt(ctx context.Context, params *acp2.PromptRequest) (*acp2.PromptResponse, error) {
	stream := acp2.NewSessionStream(a.client, params.SessionID)
	joined, err := a.StartTurn(ctx, params.SessionID, stream, func(turn context.Context, _ bareSession) acp2.StopReason {
		close(a.started)
		<-turn.Done()
		if a.ended != nil {
			a.ended <- context.Cause(turn)
		}
		return acp2.StopReasonEndTurn
	})
	if err != nil {
		return nil, err
	}
	if joined {
		return &acp2.PromptResponse{MessageID: "user_2"}, nil
	}
	return &acp2.PromptResponse{MessageID: "user_1"}, nil
}

func TestPromptsJoinAndCancelTheRunningTurn(t *testing.T) {
	agent := &cancellableAgent{
		SessionManager: acp2.NewSessionManager(acp2.NewMemoryStore[bareSession](),
			func(context.Context, *acp2.NewSessionRequest) (acp2.SessionID, bareSession, error) {
				return acp2.GenerateSessionID(), bareSession{}, nil
			}),
		started: make(chan struct{}),
	}
	_, conn := acp2.Pipe(t.Context(), func(c *acp2.AgentSideConnection) acp2.Agent {
		agent.client = c
		return agent
	}, func(*acp2.ClientSideConnection) acp2.Client { return newTestClient() })

	session, err := conn.StartSession(t.Context(), &acp2.NewSessionRequest{Cwd: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	turn, _, err := session.Prompt(t.Context(), acp2.TextBlock("work"))
	if err != nil {
		t.Fatal(err)
	}
	<-agent.started

	// A prompt sent while the work runs contributes to it: the agent joins it
	// and the client gets the same turn.
	joinedTurn, messageID, err := session.Prompt(t.Context(), acp2.TextBlock("also this"))
	if err != nil || messageID != "user_2" {
		t.Fatalf("joining prompt: %q %v", messageID, err)
	}

	if err := session.Cancel(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, tr := range []*acp2.Turn{turn, joinedTurn} {
		reason, err := tr.Wait()
		if err != nil || reason != schema.StopReasonCancelled {
			t.Fatalf("got %v %v", reason, err)
		}
	}
}

// splitAgent rejects the first prompt only after a second one has arrived and
// been accepted, then finishes the work the second prompt started.
type splitAgent struct {
	*testAgent
	firstArrived chan struct{}
	reject       chan struct{}
}

func (a *splitAgent) Prompt(ctx context.Context, params *acp2.PromptRequest) (*acp2.PromptResponse, error) {
	if text, _ := acp2.TextOf(params.Prompt[0]); text == "bad" {
		close(a.firstArrived)
		<-a.reject
		return nil, acp.InvalidParams("unsupported content")
	}
	stream := acp2.NewSessionStream(a.client, params.SessionID)
	go func() {
		ctx := context.Background()
		_ = stream.Running(ctx)
		close(a.reject) // the starter is rejected while the work runs
		_ = stream.SendText(ctx, "m1", "done")
		_ = stream.Idle(ctx, schema.StopReasonEndTurn)
	}()
	return &acp2.PromptResponse{MessageID: "user_2"}, nil
}

func TestTurnSurvivesARejectedStarter(t *testing.T) {
	agent := &splitAgent{testAgent: newTestAgent(), firstArrived: make(chan struct{}), reject: make(chan struct{})}
	_, conn := acp2.Pipe(t.Context(), func(c *acp2.AgentSideConnection) acp2.Agent {
		agent.client = c
		return agent
	}, func(*acp2.ClientSideConnection) acp2.Client { return newTestClient() })
	session, err := conn.StartSession(t.Context(), &acp2.NewSessionRequest{Cwd: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}

	firstErr := make(chan error, 1)
	go func() {
		_, _, err := session.Prompt(t.Context(), acp2.TextBlock("bad"))
		firstErr <- err
	}()
	<-agent.firstArrived // the first prompt has started the turn

	turn, messageID, err := session.Prompt(t.Context(), acp2.TextBlock("good"))
	if err != nil || messageID != "user_2" {
		t.Fatalf("joining prompt: %q %v", messageID, err)
	}
	if err := <-firstErr; !acp.IsCode(err, acp.ErrorCodeInvalidParams) {
		t.Fatalf("starter: %v", err)
	}
	text, reason, err := turn.Text()
	if err != nil || text != "done" {
		t.Fatalf("turn ended early: %q %v", text, err)
	}
	if reason != schema.StopReasonEndTurn {
		t.Fatalf("got %v", reason)
	}
}

// lateJoinAgent starts its turn only after the client's session/cancel has
// been handled, as an agent busy loading the session might.
type lateJoinAgent struct {
	*acp2.SessionManager[bareSession]
	client     acp2.Client
	received   chan struct{}
	cancelSeen chan struct{}
}

func (*lateJoinAgent) Initialize(context.Context, *acp2.InitializeRequest) (*acp2.InitializeResponse, error) {
	return &acp2.InitializeResponse{ProtocolVersion: acp2.ProtocolVersion}, nil
}

func (a *lateJoinAgent) CancelSession(ctx context.Context, params *acp2.CancelSessionNotification) error {
	defer close(a.cancelSeen)
	return a.SessionManager.CancelSession(ctx, params)
}

func (a *lateJoinAgent) Prompt(ctx context.Context, params *acp2.PromptRequest) (*acp2.PromptResponse, error) {
	close(a.received)
	<-a.cancelSeen
	turn, done, _ := a.JoinTurn(ctx, params.SessionID)
	stream := acp2.NewSessionStream(a.client, params.SessionID)
	go func() {
		defer done()
		_ = stream.Running(turn)
		reason := schema.StopReasonEndTurn
		select {
		case <-turn.Done():
			if acp.TurnCancelled(turn) {
				reason = schema.StopReasonCancelled
			}
		case <-time.After(2 * time.Second):
		}
		_ = stream.Idle(context.Background(), reason)
	}()
	return &acp2.PromptResponse{MessageID: "user_1"}, nil
}

// TestCancelBeforeJoinTurnCancelsTheTurn has session/cancel arrive after the
// prompt but before the agent starts its turn; the turn must still end as
// cancelled.
func TestCancelBeforeJoinTurnCancelsTheTurn(t *testing.T) {
	agent := &lateJoinAgent{
		SessionManager: acp2.NewSessionManager(acp2.NewMemoryStore[bareSession](),
			func(context.Context, *acp2.NewSessionRequest) (acp2.SessionID, bareSession, error) {
				return acp2.GenerateSessionID(), bareSession{}, nil
			}),
		received:   make(chan struct{}),
		cancelSeen: make(chan struct{}),
	}
	_, conn := acp2.Pipe(t.Context(), func(c *acp2.AgentSideConnection) acp2.Agent {
		agent.client = c
		return agent
	}, func(*acp2.ClientSideConnection) acp2.Client { return newTestClient() })

	session, err := conn.StartSession(t.Context(), &acp2.NewSessionRequest{Cwd: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	prompted := make(chan *acp2.Turn, 1)
	go func() {
		turn, _, err := session.Prompt(t.Context(), acp2.TextBlock("work"))
		if err != nil {
			t.Error(err)
		}
		prompted <- turn
	}()
	<-agent.received
	if err := session.Cancel(t.Context()); err != nil {
		t.Fatal(err)
	}
	turn := <-prompted
	if turn == nil {
		t.FailNow()
	}
	if reason, err := turn.Wait(); err != nil || reason != schema.StopReasonCancelled {
		t.Fatalf("got %v %v, want cancelled", reason, err)
	}
}

// panickingAgent's turn work panics.
type panickingAgent struct {
	*acp2.SessionManager[bareSession]
	client acp2.Client
}

func (panickingAgent) Initialize(context.Context, *acp2.InitializeRequest) (*acp2.InitializeResponse, error) {
	return &acp2.InitializeResponse{ProtocolVersion: acp2.ProtocolVersion}, nil
}

func (a *panickingAgent) Prompt(ctx context.Context, params *acp2.PromptRequest) (*acp2.PromptResponse, error) {
	stream := acp2.NewSessionStream(a.client, params.SessionID)
	if _, err := a.StartTurn(ctx, params.SessionID, stream, func(context.Context, bareSession) acp2.StopReason {
		panic("boom")
	}); err != nil {
		return nil, err
	}
	return &acp2.PromptResponse{MessageID: "user_1"}, nil
}

func TestPanickingTurnWorkEndsTheTurn(t *testing.T) {
	agent := &panickingAgent{SessionManager: acp2.NewSessionManager(acp2.NewMemoryStore[bareSession](),
		func(context.Context, *acp2.NewSessionRequest) (acp2.SessionID, bareSession, error) {
			return acp2.GenerateSessionID(), bareSession{}, nil
		})}
	client := &acp2test.Client{}
	conn := acp2test.Connect(t, func(c *acp2.AgentSideConnection) acp2.Agent {
		agent.client = c
		return agent
	}, client)

	session, err := conn.StartSession(t.Context(), &acp2.NewSessionRequest{Cwd: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	turn, _, err := session.Prompt(t.Context(), acp2.TextBlock("work"))
	if err != nil {
		t.Fatal(err)
	}
	reason, err := turn.Wait()
	if err != nil || reason != acp2.StopReasonInternalError {
		t.Fatalf("turn ended with %v %v, want %s", reason, err, acp2.StopReasonInternalError)
	}
	idle, err := client.WaitFor(t.Context(), func(n *acp2.UpdateSessionNotification) bool {
		_, ok := n.Update.As[acp2.SessionUpdateStateUpdate]()
		return ok && n.Meta != nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if message, _, _ := idle.Meta.Get[string]("error"); !strings.Contains(message, "boom") {
		t.Errorf("idle _meta error = %q", message)
	}
}

// TestTurnEndsWithTheConnection closes the client's connection while a turn
// runs: the turn's work sees its context end, not cancelled by the client.
func TestTurnEndsWithTheConnection(t *testing.T) {
	ended := make(chan error, 1)
	agent := &cancellableAgent{
		SessionManager: acp2.NewSessionManager(acp2.NewMemoryStore[bareSession](),
			func(context.Context, *acp2.NewSessionRequest) (acp2.SessionID, bareSession, error) {
				return acp2.GenerateSessionID(), bareSession{}, nil
			}),
		started: make(chan struct{}),
	}
	agent.ended = ended
	_, conn := acp2.Pipe(t.Context(), func(c *acp2.AgentSideConnection) acp2.Agent {
		agent.client = c
		return agent
	}, func(*acp2.ClientSideConnection) acp2.Client { return newTestClient() })

	session, err := conn.StartSession(t.Context(), &acp2.NewSessionRequest{Cwd: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := session.Prompt(t.Context(), acp2.TextBlock("work")); err != nil {
		t.Fatal(err)
	}
	<-agent.started
	conn.Close()
	select {
	case cause := <-ended:
		if cause == nil || errors.Is(cause, acp.ErrTurnCancelled) {
			t.Errorf("turn ended with cause %v, want the connection's", cause)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the turn outlived the connection")
	}
}
