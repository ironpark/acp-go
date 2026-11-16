package acp1_test

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	acp "github.com/ironpark/acp-go"
	"github.com/ironpark/acp-go/acp1"
	schema "github.com/ironpark/acp-go/schema/v1"
)

// These tests send raw messages in a fixed order, as a client other than this
// SDK's might, to pin how session/cancel is ordered against prompts: a cancel
// ends every prompt of its session sent before it and none sent after it,
// however the handlers are scheduled.

// wirePeer is the client end of an in-memory transport.
type wirePeer struct {
	t  *testing.T
	in chan jsontext.Value // to the agent

	mu        sync.Mutex
	responses map[string]chan wireMessage // by id
	requests  chan wireMessage            // from the agent
}

type wireMessage struct {
	ID     jsontext.Value `json:"id,omitzero"`
	Method string         `json:"method,omitzero"`
	Result jsontext.Value `json:"result,omitzero"`
	Error  *struct {
		Code int `json:"code"`
	} `json:"error,omitzero"`
}

func (p *wirePeer) ReadMessage(ctx context.Context) (jsontext.Value, error) {
	select {
	case msg := <-p.in:
		return msg, nil
	case <-ctx.Done():
		return nil, io.EOF
	}
}

func (p *wirePeer) WriteMessage(_ context.Context, data jsontext.Value) error {
	var msg wireMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return err
	}
	if msg.Method != "" {
		if len(msg.ID) > 0 {
			p.requests <- msg
		}
		return nil
	}
	p.response(string(msg.ID)) <- msg
	return nil
}

func (*wirePeer) Close() error { return nil }

func (p *wirePeer) response(id string) chan wireMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.responses[id] == nil {
		p.responses[id] = make(chan wireMessage, 1)
	}
	return p.responses[id]
}

func (p *wirePeer) send(format string, args ...any) {
	p.in <- jsontext.Value(fmt.Sprintf(format, args...))
}

func (p *wirePeer) prompt(id int, text string) {
	p.send(`{"jsonrpc":"2.0","id":%d,"method":"session/prompt","params":{"sessionId":"s1","prompt":[{"type":"text","text":%q}]}}`, id, text)
}

func (p *wirePeer) cancel() {
	p.send(`{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s1"}}`)
}

// await returns the answer to request id.
func (p *wirePeer) await(id int) wireMessage {
	p.t.Helper()
	select {
	case msg := <-p.response(fmt.Sprint(id)):
		return msg
	case <-time.After(5 * time.Second):
		p.t.Fatalf("request %d was never answered", id)
		return wireMessage{}
	}
}

// stopReason returns the stop reason request id, a prompt, was answered with.
func (p *wirePeer) stopReason(id int) acp1.StopReason {
	p.t.Helper()
	msg := p.await(id)
	var response acp1.PromptResponse
	if msg.Error != nil || json.Unmarshal(msg.Result, &response) != nil {
		p.t.Fatalf("prompt %d: error %+v, result %s", id, msg.Error, msg.Result)
	}
	return response.StopReason
}

// orderAgent answers the prompt "wait" once its turn is cancelled, taking a
// moment to wind down, and any other prompt at once. The notification _slow
// blocks until released, and _ask asks the client for a file and waits.
type orderAgent struct {
	*acp1.SessionManager[struct{}]
	conn    *acp1.AgentSideConnection
	waiting chan struct{} // a "wait" prompt began its turn
	release chan struct{} // lets _slow return
	entered chan string   // a blocking notification handler started
	running atomic.Int32
	overlap atomic.Bool
}

func (*orderAgent) Initialize(context.Context, *acp1.InitializeRequest) (*acp1.InitializeResponse, error) {
	return &acp1.InitializeResponse{ProtocolVersion: acp1.ProtocolVersion}, nil
}

func (a *orderAgent) Prompt(ctx context.Context, params *acp1.PromptRequest) (*acp1.PromptResponse, error) {
	return a.RunTurn(ctx, params.SessionID, func(ctx context.Context, _ struct{}) (acp1.StopReason, error) {
		if a.running.Add(1) > 1 {
			a.overlap.Store(true)
		}
		defer a.running.Add(-1)
		if acp1.JoinTexts(params.Prompt) == "wait" {
			a.waiting <- struct{}{}
			<-ctx.Done()
			time.Sleep(10 * time.Millisecond) // winding down
		}
		return schema.StopReasonEndTurn, nil
	})
}

func (a *orderAgent) ServeExtNotification(ctx context.Context, method string, _ jsontext.Value) error {
	switch method {
	case "_slow":
		a.entered <- method
		select {
		case <-a.release:
		case <-ctx.Done():
		}
	case "_ask":
		a.entered <- method
		_, _ = a.conn.ReadTextFile(ctx, &acp1.ReadTextFileRequest{SessionID: "s1", Path: "/x"})
	}
	return nil
}

func newOrderAgent(t *testing.T) (*orderAgent, *wirePeer) {
	t.Helper()
	store := acp1.NewMemoryStore[struct{}]()
	if err := store.Set(t.Context(), "s1", struct{}{}); err != nil {
		t.Fatal(err)
	}
	agent := &orderAgent{
		SessionManager: acp1.NewSessionManager(store, nil),
		waiting:        make(chan struct{}, 8),
		release:        make(chan struct{}),
		entered:        make(chan string, 8),
	}
	peer := &wirePeer{t: t, in: make(chan jsontext.Value, 64), responses: map[string]chan wireMessage{}, requests: make(chan wireMessage, 8)}
	conn := acp1.NewAgentSideConnection(func(c *acp1.AgentSideConnection) acp1.Agent {
		agent.conn = c
		return agent
	}, peer)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = conn.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-conn.Done()
	})
	return agent, peer
}

func waitFor[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("%s never happened", what)
		panic("unreachable")
	}
}

// A cancel queued behind a slow notification is handled after a later prompt
// has been read. It still ends only the prompt sent before it.
func TestCancelBehindSlowNotificationSparesLaterPrompt(t *testing.T) {
	agent, peer := newOrderAgent(t)
	peer.prompt(1, "wait")
	waitFor(t, agent.waiting, "the first turn")
	peer.send(`{"jsonrpc":"2.0","method":"_slow"}`)
	waitFor(t, agent.entered, "the slow notification")
	peer.cancel()
	peer.prompt(2, "quick")

	// The cancel reaches the first prompt though its handler is still queued.
	if reason := peer.stopReason(1); reason != schema.StopReasonCancelled {
		t.Errorf("prompt sent before the cancel ended %q, want cancelled", reason)
	}
	close(agent.release)
	if reason := peer.stopReason(2); reason != schema.StopReasonEndTurn {
		t.Errorf("prompt sent after the cancel ended %q, want end_turn", reason)
	}
	if agent.overlap.Load() {
		t.Error("two turns of one session ran together")
	}
}

// The same with a notification handler that waits on the client: the cancel
// and the later prompt are read while the handler awaits its answer.
func TestCancelBehindNotificationAwaitingClientSparesLaterPrompt(t *testing.T) {
	agent, peer := newOrderAgent(t)
	peer.prompt(1, "wait")
	waitFor(t, agent.waiting, "the first turn")
	peer.send(`{"jsonrpc":"2.0","method":"_ask"}`)
	waitFor(t, agent.entered, "the asking notification")
	ask := waitFor(t, peer.requests, "the agent's request")
	peer.cancel()
	peer.prompt(2, "quick")
	if !strings.Contains(ask.Method, "read_text_file") {
		t.Fatalf("agent requested %q", ask.Method)
	}
	peer.send(`{"jsonrpc":"2.0","id":%s,"result":{"content":""}}`, ask.ID)

	if reason := peer.stopReason(1); reason != schema.StopReasonCancelled {
		t.Errorf("prompt sent before the cancel ended %q, want cancelled", reason)
	}
	if reason := peer.stopReason(2); reason != schema.StopReasonEndTurn {
		t.Errorf("prompt sent after the cancel ended %q, want end_turn", reason)
	}
}

// A client that does not wait for the cancelled prompt's answer sends the
// next prompt right behind the cancel. The new turn starts once the cancelled
// one has ended, never beside it, and is not cancelled itself.
func TestPromptRightBehindCancelRunsAfterTheCancelledTurn(t *testing.T) {
	agent, peer := newOrderAgent(t)
	for round := range 30 {
		first, second := 2*round+1, 2*round+2
		peer.prompt(first, "wait")
		waitFor(t, agent.waiting, "the first turn")
		peer.cancel()
		peer.prompt(second, "quick")
		if reason := peer.stopReason(first); reason != schema.StopReasonCancelled {
			t.Fatalf("round %d: prompt before the cancel ended %q, want cancelled", round, reason)
		}
		if reason := peer.stopReason(second); reason != schema.StopReasonEndTurn {
			t.Fatalf("round %d: prompt after the cancel ended %q, want end_turn", round, reason)
		}
	}
	if agent.overlap.Load() {
		t.Error("a turn started while the cancelled one was still winding down")
	}
}

// session/cancel is a notification. Sent as a request it is not a method the
// agent serves: it is answered "method not found" and cancels nothing, as in
// the TypeScript SDK.
func TestSessionCancelSentAsARequestCancelsNothing(t *testing.T) {
	agent, peer := newOrderAgent(t)
	peer.prompt(1, "wait")
	waitFor(t, agent.waiting, "the turn")
	peer.send(`{"jsonrpc":"2.0","id":9,"method":"session/cancel","params":{"sessionId":"s1"}}`)
	if msg := peer.await(9); msg.Error == nil || msg.Error.Code != int(acp.ErrorCodeMethodNotFound) {
		t.Fatalf("session/cancel request answered %+v, want method not found", msg)
	}
	select {
	case msg := <-peer.response("1"):
		t.Fatalf("the prompt ended on a session/cancel request: %s", msg.Result)
	case <-time.After(50 * time.Millisecond):
	}
	peer.cancel()
	if reason := peer.stopReason(1); reason != schema.StopReasonCancelled {
		t.Errorf("prompt ended %q after the cancel notification, want cancelled", reason)
	}
}
