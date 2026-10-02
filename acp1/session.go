package acp1

import (
	"context"
	"iter"
	"strings"

	"github.com/ironpark/acp-go/internal/acpconn"
	schema "github.com/ironpark/acp-go/schema/v1"
)

// ClientSession drives prompt turns on one session from the client side:
//
//	session, err := agent.StartSession(ctx, &acp1.NewSessionRequest{Cwd: cwd})
//	turn, err := session.Prompt(ctx, acp1.TextBlock("Summarize README.md"))
//	for update := range turn.Updates() {
//		// render tool calls, plans, message chunks...
//	}
//	response, err := turn.Wait()
//
// The client's [Client.SessionUpdate] still receives every update; a turn
// sees a copy of those that arrive while it runs.
type ClientSession struct {
	ID   SessionID
	conn *ClientSideConnection
}

// StartSession creates a session and returns a handle for prompting it. Use
// [ClientSideConnection.NewSession] instead when the response's modes or
// config options are needed, then [ClientSideConnection.Session].
func (c *ClientSideConnection) StartSession(ctx context.Context, params *NewSessionRequest) (*ClientSession, error) {
	response, err := c.NewSession(ctx, params)
	if err != nil {
		return nil, err
	}
	return c.Session(response.SessionID), nil
}

// Session returns a handle for a session the connection already knows, such
// as one created with NewSession or restored with LoadSession.
func (c *ClientSideConnection) Session(id SessionID) *ClientSession {
	return &ClientSession{ID: id, conn: c}
}

// Prompt starts a turn and returns once the prompt is on its way; the turn
// ends when the agent answers the prompt. A [ClientSession.Cancel] after
// Prompt returns reaches the agent after the prompt. A v1 session runs one
// turn at a time, so Prompt fails with [acp.ErrTurnInProgress] until the
// previous turn has ended.
//
// ctx governs the whole turn, as the prompt request lasts until the turn
// ends. When ctx is cancelled or its deadline passes, Prompt cancels the turn
// the way the protocol intends, with session/cancel as [ClientSession.Cancel]
// sends, and the turn ends once the agent answers, with
// [StopReasonCancelled]. The connection's request timeout does not apply to
// it; give ctx a deadline to bound a turn:
//
//	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
//	defer cancel()
//	turn, err := session.Prompt(ctx, acp1.TextBlock("Fix the failing test"))
//	response, err := turn.Wait() // before cancel runs
//
// Cancelling ctx before the turn ends cancels it, so keep ctx alive while
// the turn runs.
func (s *ClientSession) Prompt(ctx context.Context, content ...ContentBlock) (*Turn, error) {
	t, err := s.conn.turns.Begin(s.ID)
	if err != nil {
		return nil, err
	}
	// The request is cancelled with session/cancel, never abandoned, so its
	// answer still ends the turn.
	wait, err := acpconn.StartCall[PromptResponse](context.WithoutCancel(ctx), s.conn.conn, schema.AgentMethodsSessionPrompt,
		&PromptRequest{SessionID: s.ID, Prompt: content})
	if err != nil {
		s.conn.turns.End(s.ID, t, nil, err)
		return nil, err
	}
	// Queued after the prompt, so the cancel always reaches it.
	stop := context.AfterFunc(ctx, func() { _ = s.Cancel(context.WithoutCancel(ctx)) })
	go func() {
		response, err := wait()
		stop()
		s.conn.turns.End(s.ID, t, response, err)
	}()
	return &Turn{t: t}, nil
}

// Cancel asks the agent to stop the session's current turn; the turn then
// ends with [StopReasonCancelled].
func (s *ClientSession) Cancel(ctx context.Context) error {
	return s.conn.CancelSession(ctx, &CancelNotification{SessionID: s.ID})
}

// Turn is one prompt turn started by [ClientSession.Prompt].
type Turn struct {
	t *acpconn.Turn[SessionUpdate, *PromptResponse]
}

// Updates yields the turn's session updates in order and stops when the turn
// ends. Updates that arrived before the call are included. Only one reader
// should range over it.
func (t *Turn) Updates() iter.Seq[SessionUpdate] { return t.t.Updates() }

// Wait blocks until the turn ends and returns the agent's response.
func (t *Turn) Wait() (*PromptResponse, error) { return t.t.Wait() }

// Done is closed once the turn ends.
func (t *Turn) Done() <-chan struct{} { return t.t.Done() }

// Text consumes the turn's updates and returns the agent's message text, for
// callers that only want the answer, with the same response and error as Wait.
func (t *Turn) Text() (string, *PromptResponse, error) {
	var b strings.Builder
	for update := range t.Updates() {
		if chunk, ok := update.As[schema.SessionUpdateAgentMessageChunk](); ok {
			if text, ok := TextOf(chunk.Content); ok {
				b.WriteString(text)
			}
		}
	}
	response, err := t.Wait()
	return b.String(), response, err
}
