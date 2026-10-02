package acp2

import (
	"context"
	"errors"
	"iter"
	"strings"

	"github.com/ironpark/acp-go/internal/acpconn"
	schema "github.com/ironpark/acp-go/schema/v2"
)

// ClientSession drives prompt turns on one session from the client side:
//
//	session, err := agent.StartSession(ctx, &acp2.NewSessionRequest{Cwd: cwd})
//	turn, messageID, err := session.Prompt(ctx, acp2.TextBlock("Summarize README.md"))
//	for update := range turn.Updates() {
//		// render tool calls, plans, messages...
//	}
//	stopReason, err := turn.Wait()
//
// The client's [Client.SessionUpdate] still receives every update; a turn
// sees a copy of those that arrive while it runs.
type ClientSession struct {
	ID   SessionID
	conn *ClientSideConnection
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

// Session returns a handle for a session the connection already knows, such
// as one created with NewSession or resumed with ResumeSession.
func (c *ClientSideConnection) Session(id SessionID) *ClientSession {
	return &ClientSession{ID: id, conn: c}
}

var errConnectionClosed = errors.New("acp2: connection closed before the turn ended")

// Prompt sends a user message and returns once the agent has accepted it,
// that is inserted it into the conversation, with the message's id. A
// rejected prompt returns the agent's error.
//
// The returned [Turn] is the session's foreground work, which ends when the
// agent reports idle. v2 lets a prompt contribute to work already running, so
// a prompt sent mid-turn joins it and returns the same turn.
func (s *ClientSession) Prompt(ctx context.Context, content ...ContentBlock) (*Turn, MessageID, error) {
	t, created := s.conn.turns.Join(s.ID)
	if created {
		go s.watch(t)
	}
	response, err := s.conn.Prompt(ctx, &PromptRequest{SessionID: s.ID, Prompt: content})
	// A rejected prompt ends the turn only if no prompt that joined it was
	// accepted; otherwise the work runs on until idle.
	s.conn.turns.Settle(s.ID, t, err)
	if err != nil {
		return nil, "", err
	}
	return &Turn{t: t}, response.MessageID, nil
}

// watch ends t if the connection closes first. The agent's idle update ends
// it in [ClientSideConnection.sessionUpdate], and Settle when every prompt
// that joined it was rejected.
func (s *ClientSession) watch(t *acpconn.Turn[SessionUpdate, StopReason]) {
	select {
	case <-s.conn.Done():
		s.conn.turns.End(s.ID, t, "", errConnectionClosed)
	case <-t.Done():
	}
}

// Cancel asks the agent to stop the session's foreground work; the turn then
// ends with [StopReasonCancelled].
func (s *ClientSession) Cancel(ctx context.Context) error {
	return s.conn.CancelSession(ctx, &CancelSessionNotification{SessionID: s.ID})
}

// Turn is a session's foreground work, from the prompt that started it until
// the agent reports idle. Prompts that join it share it.
type Turn struct {
	t *acpconn.Turn[SessionUpdate, StopReason]
}

// Updates yields the turn's session updates in order and stops when the turn
// ends. Updates that arrived before the call are included. Only one reader
// should range over it. The reader is not ordered with the agent's requests:
// a permission request can be handled before the reader reaches the update
// it follows. A client that shows both renders in [Client.SessionUpdate],
// which the connection handles first.
func (t *Turn) Updates() iter.Seq[SessionUpdate] { return t.t.Updates() }

// Wait blocks until the turn ends and returns the stop reason from the
// agent's idle update, or "" if it gave none.
func (t *Turn) Wait() (StopReason, error) { return t.t.Wait() }

// Done is closed once the turn ends.
func (t *Turn) Done() <-chan struct{} { return t.t.Done() }

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
			if u.Content != nil {
				b := message(u.MessageID)
				b.Reset()
				for text := range Texts(u.Content) {
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
