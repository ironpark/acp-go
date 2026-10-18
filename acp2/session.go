package acp2

import (
	"context"
	"errors"
	"iter"
	"strings"

	"github.com/ironpark/acp-go/internal/acpconn"
	schema "github.com/ironpark/acp-go/schema/v2"
)

// ClientSession submits prompts to one session and reads its updates from the
// client side:
//
//	session, err := agent.StartSession(ctx, &acp2.NewSessionRequest{Cwd: cwd})
//	messageID, err := session.Prompt(ctx, acp2.TextBlock("Summarize README.md"))
//	turn := session.UntilIdle(ctx)
//	for update := range turn.Updates() {
//		// render tool calls, plans, messages...
//	}
//	stopReason, err := turn.Wait()
//
// In v2 the prompt response only reports that the message was inserted into
// the conversation, and the agent's idle state update reports that the
// session's foreground work stopped, not that a given prompt was processed.
// So the session's updates are queued apart from its prompts, from the moment
// the ClientSession is created, and read in arrival order. The client's
// [Client.SessionUpdate] still receives every update.
type ClientSession struct {
	ID      SessionID
	conn    *ClientSideConnection
	updates *acpconn.Queue[SessionUpdate]
}

// StartSession creates a session and returns a handle for prompting it. Use
// [ClientSideConnection.NewSession] instead when the response's other fields
// are needed, then [ClientSideConnection.Session].
func (c *ClientSideConnection) StartSession(ctx context.Context, params *NewSessionRequest) (*ClientSession, error) {
	response, err := c.NewSession(ctx, params)
	if err != nil {
		return nil, err
	}
	return c.Session(response.SessionID), nil
}

var (
	errConnectionClosed = errors.New("acp2: connection closed")
	errSessionClosed    = errors.New("acp2: session closed")
)

// Session returns a handle for a session the connection already knows, such
// as one created with NewSession or resumed with ResumeSession, and starts
// queueing its updates; earlier ones reach only [Client.SessionUpdate]. Every
// call for the same id shares one queue, which [ClientSession.Close] releases.
func (c *ClientSideConnection) Session(id SessionID) *ClientSession {
	c.mu.Lock()
	defer c.mu.Unlock()
	q := c.sessions[id]
	if q == nil {
		if c.sessions == nil {
			c.sessions = map[SessionID]*acpconn.Queue[SessionUpdate]{}
		}
		q = acpconn.NewQueue[SessionUpdate](c.Done(), errConnectionClosed)
		c.sessions[id] = q
	}
	return &ClientSession{ID: id, conn: c, updates: q}
}

// Prompt sends a user message and returns once the agent has inserted it into
// the conversation, with the message's id. A rejected prompt returns the
// agent's error, which affects no reader of the session's updates. Read what
// the agent does with the message through [ClientSession.UntilIdle] or
// [ClientSession.NextUpdate].
func (s *ClientSession) Prompt(ctx context.Context, content ...ContentBlock) (MessageID, error) {
	response, err := s.conn.Prompt(ctx, &PromptRequest{SessionID: s.ID, Prompt: content})
	if err != nil {
		return "", err
	}
	return response.MessageID, nil
}

// NextUpdate returns the session's oldest queued update, waiting for one if
// needed. Idle state updates are returned like any other. A cancelled ctx
// ends the wait without cancelling the agent's work or discarding updates.
func (s *ClientSession) NextUpdate(ctx context.Context) (SessionUpdate, error) {
	return s.updates.Next(ctx)
}

// UntilIdle returns a reader of the session's queued updates up to and
// including the next idle state update, whichever prompt they follow. Only
// one reader should consume a session's updates at a time.
func (s *ClientSession) UntilIdle(ctx context.Context) *Turn {
	return &Turn{ctx: ctx, updates: s.updates}
}

// Cancel asks the agent to stop the session's foreground work; it then
// reports idle with [StopReasonCancelled].
func (s *ClientSession) Cancel(ctx context.Context) error {
	return s.conn.CancelSession(ctx, &CancelSessionNotification{SessionID: s.ID})
}

// Close closes the session on the agent and stops queueing its updates.
// Updates already queued can still be read.
func (s *ClientSession) Close(ctx context.Context) error {
	if _, err := s.conn.CloseSession(ctx, &CloseSessionRequest{SessionID: s.ID}); err != nil {
		return err
	}
	s.conn.mu.Lock()
	if s.conn.sessions[s.ID] == s.updates {
		delete(s.conn.sessions, s.ID)
	}
	s.conn.mu.Unlock()
	s.updates.Close(errSessionClosed)
	return nil
}

// Turn reads a session's foreground work: its updates up to the next idle
// state update. It is not tied to a prompt; see [ClientSession].
type Turn struct {
	ctx     context.Context
	updates *acpconn.Queue[SessionUpdate]
	ended   bool
	reason  StopReason
	err     error
}

// Updates yields the session's updates in arrival order, including those
// queued before the call, and stops after the idle state update. It also
// stops when the context given to [ClientSession.UntilIdle] is done or the
// connection closes, which Wait then reports. The reader is not ordered with
// the agent's requests: a permission request can be handled before the reader
// reaches the update it follows. A client that shows both renders in
// [Client.SessionUpdate], which the connection handles first.
func (t *Turn) Updates() iter.Seq[SessionUpdate] {
	return func(yield func(SessionUpdate) bool) {
		for !t.ended {
			update, err := t.updates.Next(t.ctx)
			if err != nil {
				t.ended, t.err = true, err
				return
			}
			if idle, ok := idleOf(update); ok {
				t.ended = true
				t.reason, t.err = stopOf(idle.Value)
			}
			if !yield(update) {
				return
			}
		}
	}
}

// Wait consumes the remaining updates up to the idle state update and returns
// the stop reason it reports, or "" if it gave none.
func (t *Turn) Wait() (StopReason, error) {
	for range t.Updates() {
	}
	return t.reason, t.err
}

// idleOf returns the idle state an update reports, if it is one.
func idleOf(update SessionUpdate) (schema.StateUpdateIdle, bool) {
	state, ok := update.As[schema.SessionUpdateStateUpdate]()
	if !ok {
		return schema.StateUpdateIdle{}, false
	}
	return state.Value.As[schema.StateUpdateIdle]()
}

// Text consumes the turn's updates and returns the text of the agent's
// messages in order, applying chunks and full-message updates by message id,
// with the same stop reason and error as Wait.
func (t *Turn) Text() (string, StopReason, error) {
	var order []MessageID
	texts := map[MessageID]*strings.Builder{}
	message := func(id MessageID) *strings.Builder {
		b, ok := texts[id]
		if !ok {
			b = &strings.Builder{}
			texts[id] = b
			order = append(order, id)
		}
		return b
	}
	for update := range t.Updates() {
		switch u := update.Variant().(type) {
		case schema.SessionUpdateAgentMessageChunk:
			if text, ok := TextOf(u.Content); ok {
				message(u.MessageID).WriteString(text)
			}
		case schema.SessionUpdateAgentMessage:
			if content, ok := u.Content.Get(); ok {
				b := message(u.MessageID)
				b.Reset()
				for text := range Texts(content) {
					b.WriteString(text)
				}
			}
		}
	}
	var out strings.Builder
	for _, id := range order {
		out.WriteString(texts[id].String())
	}
	reason, err := t.Wait()
	return out.String(), reason, err
}
