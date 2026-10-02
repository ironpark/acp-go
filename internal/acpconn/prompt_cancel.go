package acpconn

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"slices"
	"sync"

	"github.com/ironpark/acp-go/internal/jsonrpc"
)

// ErrTurnCancelled is the cause of a turn context cancelled by the client's
// session/cancel.
var ErrTurnCancelled = errors.New("acp: turn cancelled by the client")

// Both protocol versions name these methods the same.
const (
	methodSessionPrompt = "session/prompt"
	methodSessionCancel = "session/cancel"
)

// NewAgentConnection builds the JSON-RPC connection behind an agent-side
// façade over transport. It also lets a session/cancel reach a prompt that
// arrived before it but whose handler has not started its turn yet: requests
// run in their own goroutines, so without this the cancel could find no turn
// to cancel and be lost. Both are matched up on the read loop, in wire order,
// so a cancel reaches exactly the prompts sent before it and never a later
// one. See [PromptCancelSignal].
func NewAgentConnection(request jsonrpc.RequestHandler, notification jsonrpc.NotificationHandler, transport jsonrpc.Transport, opts []jsonrpc.Option) *jsonrpc.Connection {
	prompts := &pendingPrompts{sessions: map[string][]*pendingPrompt{}}
	received := func(method string, params jsontext.Value) {
		if method == methodSessionCancel {
			prompts.cancel(sessionIDOf(params))
		}
	}
	opts = append(slices.Clone(opts), jsonrpc.WithRequestContext(prompts.accept), jsonrpc.WithNotificationReceived(received))
	return jsonrpc.New(request, notification, transport, opts...)
}

// PromptCancelSignal returns the signal a session/prompt request's context
// carries, or nil for any other context. The signal is cancelled with
// [ErrTurnCancelled] when a session/cancel for the prompt's session arrives
// while the prompt is outstanding, even before its turn has started. Contexts
// derived from the request's, including detached ones, carry it too.
func PromptCancelSignal(ctx context.Context) context.Context {
	signal, _ := ctx.Value(promptCancelKey{}).(context.Context)
	return signal
}

type promptCancelKey struct{}

// pendingPrompts tracks the outstanding session/prompt requests by session.
type pendingPrompts struct {
	mu       sync.Mutex
	sessions map[string][]*pendingPrompt
}

type pendingPrompt struct{ cancel context.CancelCauseFunc }

// accept registers a session/prompt request until its handler returns, and
// attaches its cancel signal to the request's context.
func (p *pendingPrompts) accept(ctx context.Context, method string, params jsontext.Value) context.Context {
	if method != methodSessionPrompt {
		return ctx
	}
	id := sessionIDOf(params)
	if id == "" {
		return ctx
	}
	// The signal is never cancelled but by session/cancel, so a turn that
	// outlives the request is not cancelled when the request ends.
	signal, cancel := context.WithCancelCause(context.Background())
	prompt := &pendingPrompt{cancel: cancel}
	p.mu.Lock()
	p.sessions[id] = append(p.sessions[id], prompt)
	p.mu.Unlock()
	context.AfterFunc(ctx, func() { p.remove(id, prompt) })
	return context.WithValue(ctx, promptCancelKey{}, signal)
}

func (p *pendingPrompts) remove(id string, prompt *pendingPrompt) {
	p.mu.Lock()
	defer p.mu.Unlock()
	remaining := slices.DeleteFunc(p.sessions[id], func(q *pendingPrompt) bool { return q == prompt })
	if len(remaining) == 0 {
		delete(p.sessions, id)
	} else {
		p.sessions[id] = remaining
	}
}

// cancel signals every outstanding prompt of a session.
func (p *pendingPrompts) cancel(id string) {
	p.mu.Lock()
	prompts := slices.Clone(p.sessions[id])
	p.mu.Unlock()
	for _, prompt := range prompts {
		prompt.cancel(ErrTurnCancelled)
	}
}

// sessionIDOf reads the sessionId member of a request's params, or "" if
// there is none.
func sessionIDOf(params jsontext.Value) string {
	var p struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(params, &p) != nil {
		return ""
	}
	return p.SessionID
}
