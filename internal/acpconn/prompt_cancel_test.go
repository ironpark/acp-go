package acpconn

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/ironpark/acp-go/internal/jsonrpc"
)

func TestSessionIDOf(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{`{"sessionId":"s1"}`, "s1"},
		{`{"sessionId":""}`, ""},
		{`{}`, ""},
		{`not json`, ""},
		{``, ""},
	}
	for _, c := range cases {
		if got := sessionIDOf(jsontext.Value(c.raw)); got != c.want {
			t.Errorf("sessionIDOf(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestPromptCancelSignal(t *testing.T) {
	p := &pendingPrompts{sessions: map[string][]*pendingPrompt{}}
	ctx := p.accept(context.Background(), methodSessionPrompt, jsontext.Value(`{"sessionId":"s1"}`))

	signal := PromptCancelSignal(ctx)
	if signal == nil {
		t.Fatal("a prompt request should carry a cancel signal")
	}
	select {
	case <-signal.Done():
		t.Fatal("the signal was cancelled before any session/cancel")
	default:
	}

	p.cancel("s1")
	if cause := context.Cause(signal); !errors.Is(cause, ErrTurnCancelled) {
		t.Fatalf("signal cause = %v, want ErrTurnCancelled", cause)
	}
}

func TestPromptCancelIgnoresOtherMethods(t *testing.T) {
	p := &pendingPrompts{sessions: map[string][]*pendingPrompt{}}
	ctx := p.accept(context.Background(), methodSessionCancel, jsontext.Value(`{"sessionId":"s1"}`))
	if PromptCancelSignal(ctx) != nil {
		t.Fatal("a non-prompt request should not carry a cancel signal")
	}
	if len(p.sessions) != 0 {
		t.Fatalf("a non-prompt request was tracked: %v", p.sessions)
	}
}

func TestAcceptIgnoresPromptWithoutSession(t *testing.T) {
	p := &pendingPrompts{sessions: map[string][]*pendingPrompt{}}
	ctx := p.accept(context.Background(), methodSessionPrompt, jsontext.Value(`{}`))
	if PromptCancelSignal(ctx) != nil {
		t.Fatal("a prompt without a session id should not carry a cancel signal")
	}
	if len(p.sessions) != 0 {
		t.Fatalf("a prompt without a session id was tracked: %v", p.sessions)
	}
}

func TestRemoveDropsOnlyTheNamedPrompt(t *testing.T) {
	p := &pendingPrompts{sessions: map[string][]*pendingPrompt{}}
	p.accept(context.Background(), methodSessionPrompt, jsontext.Value(`{"sessionId":"s1"}`))
	p.accept(context.Background(), methodSessionPrompt, jsontext.Value(`{"sessionId":"s1"}`))

	p.mu.Lock()
	first := p.sessions["s1"][0]
	p.mu.Unlock()

	p.remove("s1", first)
	p.mu.Lock()
	remaining := len(p.sessions["s1"])
	p.mu.Unlock()
	if remaining != 1 {
		t.Fatalf("remove left %d prompts, want 1", remaining)
	}

	p.mu.Lock()
	last := p.sessions["s1"][0]
	p.mu.Unlock()
	p.remove("s1", last)
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.sessions["s1"]; ok {
		t.Fatal("removing the last prompt should drop the session entry")
	}
}

// TestCancelReachesOnlyEarlierPrompts: notifications are handled off the read
// loop, so a session/cancel queued behind a slow notification is handled after
// a later prompt has been read. It must still cancel only the prompt sent
// before it.
func TestCancelReachesOnlyEarlierPrompts(t *testing.T) {
	local, remote := net.Pipe()
	t.Cleanup(func() { local.Close(); remote.Close() })
	go func() { _, _ = io.Copy(io.Discard, remote) }()

	gate, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(release) })
	// Each prompt runs in its own goroutine, so they may start in either
	// order; params carry which prompt it is.
	type started struct {
		params string
		signal context.Context
	}
	signals := make(chan started, 2)
	conn := NewAgentConnection(func(ctx context.Context, _ string, params jsontext.Value) (any, error) {
		signals <- started{string(params), PromptCancelSignal(ctx)}
		<-release // keep the prompt outstanding
		return nil, nil
	}, func(ctx context.Context, method string, _ jsontext.Value) error {
		if method == "slow" {
			select {
			case <-gate:
			case <-ctx.Done():
			}
		}
		return nil
	}, jsonrpc.NewStdioTransport(local, local), nil)
	go func() { _ = conn.Start(t.Context()) }()

	for _, line := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"session/prompt","params":{"sessionId":"s1","n":1}}`,
		`{"jsonrpc":"2.0","method":"slow"}`,
		`{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s1"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"s1","n":2}}`,
	} {
		if _, err := remote.Write([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	close(gate)

	byPrompt := map[bool]context.Context{} // by whether it is the first
	for range 2 {
		select {
		case s := <-signals:
			byPrompt[strings.Contains(s.params, `"n":1`)] = s.signal
		case <-time.After(2 * time.Second):
			t.Fatal("a prompt never started")
		}
	}
	// Let the cancel, if it were still pending, land.
	time.Sleep(20 * time.Millisecond)
	for first, wantCancelled := range map[bool]bool{true: true, false: false} {
		if cancelled := errors.Is(context.Cause(byPrompt[first]), ErrTurnCancelled); cancelled != wantCancelled {
			t.Errorf("first prompt %v: cancelled = %v, want %v", first, cancelled, wantCancelled)
		}
	}
}
