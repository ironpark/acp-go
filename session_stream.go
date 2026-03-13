package acp

import "context"

// SessionStream provides convenience methods for sending session updates.
//
// It wraps a Client and a session ID to reduce boilerplate when sending
// common update patterns like streaming text, tool call lifecycle, etc.
//
// Example:
//
//	stream := acp.NewSessionStream(client, sessionID)
//	stream.SendText(ctx, "Hello, ")
//	stream.SendText(ctx, "world!")
//	stream.StartToolCall(ctx, toolID, "Reading file", acp.ToolKindRead)
//	stream.CompleteToolCall(ctx, toolID, acp.NewToolCallContentContent(acp.NewContentBlockText("done")))
type SessionStream struct {
	client    Client
	sessionID SessionID
}

// NewSessionStream creates a new SessionStream for the given session.
func NewSessionStream(client Client, sessionID SessionID) *SessionStream {
	return &SessionStream{
		client:    client,
		sessionID: sessionID,
	}
}

// SendText sends an agent message chunk with text content.
func (s *SessionStream) SendText(ctx context.Context, text string) error {
	return s.send(ctx, NewSessionUpdateAgentMessageChunk(NewContentBlockText(text)))
}

// SendImage sends an agent message chunk with image content.
func (s *SessionStream) SendImage(ctx context.Context, data, mimeType, uri string) error {
	return s.send(ctx, NewSessionUpdateAgentMessageChunk(NewContentBlockImage(data, mimeType, uri)))
}

// SendThought sends an agent thought chunk with text content.
func (s *SessionStream) SendThought(ctx context.Context, text string) error {
	return s.send(ctx, NewSessionUpdateAgentThoughtChunk(NewContentBlockText(text)))
}

// SendUserMessage sends a user message chunk with text content.
func (s *SessionStream) SendUserMessage(ctx context.Context, text string) error {
	return s.send(ctx, NewSessionUpdateUserMessageChunk(NewContentBlockText(text)))
}

// StartToolCall sends a tool_call update with the given parameters.
func (s *SessionStream) StartToolCall(ctx context.Context, id ToolCallID, title string, kind ToolKind, locations ...ToolCallLocation) error {
	status := ToolCallStatusInProgress
	tc := ToolCall{
		ToolCallID: id,
		Title:      title,
		Kind:       &kind,
		Status:     &status,
	}
	if len(locations) > 0 {
		tc.Locations = locations
	}
	return s.send(ctx, NewSessionUpdateToolCall(tc))
}

// UpdateToolCallStatus sends a tool_call_update with the given status.
func (s *SessionStream) UpdateToolCallStatus(ctx context.Context, id ToolCallID, status ToolCallStatus) error {
	return s.send(ctx, NewSessionUpdateToolCallUpdate(ToolCallUpdate{
		ToolCallID: id,
		Status:     &status,
	}))
}

// CompleteToolCall sends a tool_call_update with completed status and optional content.
func (s *SessionStream) CompleteToolCall(ctx context.Context, id ToolCallID, content ...ToolCallContent) error {
	status := ToolCallStatusCompleted
	update := ToolCallUpdate{
		ToolCallID: id,
		Status:     &status,
	}
	if len(content) > 0 {
		update.Content = content
	}
	return s.send(ctx, NewSessionUpdateToolCallUpdate(update))
}

// FailToolCall sends a tool_call_update with failed status.
func (s *SessionStream) FailToolCall(ctx context.Context, id ToolCallID) error {
	status := ToolCallStatusFailed
	return s.send(ctx, NewSessionUpdateToolCallUpdate(ToolCallUpdate{
		ToolCallID: id,
		Status:     &status,
	}))
}

// SendPlan sends a plan update.
func (s *SessionStream) SendPlan(ctx context.Context, entries []PlanEntry) error {
	return s.send(ctx, NewSessionUpdatePlan(entries))
}

// SendModeUpdate sends a current mode update.
func (s *SessionStream) SendModeUpdate(ctx context.Context, modeID SessionModeID) error {
	return s.send(ctx, NewSessionUpdateCurrentModeUpdate(modeID))
}

// SendConfigUpdate sends a config option update.
func (s *SessionStream) SendConfigUpdate(ctx context.Context, options []SessionConfigOption) error {
	return s.send(ctx, NewSessionUpdateConfigOptionUpdate(options))
}

// SendSessionInfo sends a session info update.
func (s *SessionStream) SendSessionInfo(ctx context.Context, title, updatedAt string) error {
	return s.send(ctx, NewSessionUpdateSessionInfoUpdate(title, updatedAt))
}

// SendCommands sends an available commands update.
func (s *SessionStream) SendCommands(ctx context.Context, commands []AvailableCommand) error {
	return s.send(ctx, NewSessionUpdateAvailableCommandsUpdate(commands))
}

// SendUpdate sends an arbitrary SessionUpdate (escape hatch for custom updates).
func (s *SessionStream) SendUpdate(ctx context.Context, update SessionUpdate) error {
	return s.send(ctx, update)
}

func (s *SessionStream) send(ctx context.Context, update SessionUpdate) error {
	return s.client.SessionUpdate(ctx, &SessionNotification{
		SessionID: s.sessionID,
		Update:    update,
	})
}
