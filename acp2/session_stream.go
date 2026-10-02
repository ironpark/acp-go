package acp2

import (
	"context"
	"encoding/json/jsontext"

	"github.com/ironpark/acp-go/schema/optional"
	schema "github.com/ironpark/acp-go/schema/v2"
)

// ToolCallOption sets optional fields on the tool call updates
// [SessionStream.StartToolCall], [SessionStream.UpdateToolCallStatus],
// [SessionStream.CompleteToolCall] and [SessionStream.FailToolCall] send.
type ToolCallOption func(*toolCallOptions)

type toolCallOptions struct {
	content   []ToolCallContent
	locations []ToolCallLocation
	rawInput  jsontext.Value
	rawOutput jsontext.Value
}

// WithToolContent sets the tool call's content: its output, a diff or a
// terminal.
func WithToolContent(content ...ToolCallContent) ToolCallOption {
	return func(o *toolCallOptions) { o.content = append(o.content, content...) }
}

// WithLocations sets the files the tool call works on, so the client can
// follow along.
func WithLocations(locations ...ToolCallLocation) ToolCallOption {
	return func(o *toolCallOptions) { o.locations = append(o.locations, locations...) }
}

// WithRawInput sets the raw input the tool was called with.
func WithRawInput(raw jsontext.Value) ToolCallOption {
	return func(o *toolCallOptions) { o.rawInput = raw }
}

// WithRawOutput sets the raw output the tool returned.
func WithRawOutput(raw jsontext.Value) ToolCallOption {
	return func(o *toolCallOptions) { o.rawOutput = raw }
}

// toolCallUpdate builds a tool call update with status and the fields opts set.
func toolCallUpdate(id ToolCallID, status ToolCallStatus, opts []ToolCallOption) schema.SessionUpdateToolCallUpdate {
	var o toolCallOptions
	for _, opt := range opts {
		opt(&o)
	}
	update := schema.SessionUpdateToolCallUpdate{
		ToolCallID: id,
		Status:     optional.Of(status),
		RawInput:   o.rawInput,
		RawOutput:  o.rawOutput,
	}
	if o.content != nil {
		update.Content = optional.Of(o.content)
	}
	if o.locations != nil {
		update.Locations = optional.Of(o.locations)
	}
	return update
}

// SessionStream sends session/update notifications for one session.
//
// It removes the boilerplate of naming the session and building a
// [SessionUpdate] variant for each update. v2 keys every message by id and
// makes the turn state explicit, so a typical turn reads:
//
//	stream := acp2.NewSessionStream(client, sessionID)
//	stream.Running(ctx)
//	stream.SendText(ctx, messageID, "Reading the file…")
//	stream.StartToolCall(ctx, toolID, "Read file", acp2.ToolKindRead)
//	stream.CompleteToolCall(ctx, toolID, acp2.WithToolContent(acp2.ToolText(contents)))
//	stream.Idle(ctx, acp2.StopReasonEndTurn)
//
// Use [SessionStream.Send] for any update the helpers do not cover.
type SessionStream struct {
	client    Client
	sessionID SessionID
	meta      Meta
}

// NewSessionStream binds a client to a session id.
func NewSessionStream(client Client, sessionID SessionID) *SessionStream {
	return &SessionStream{client: client, sessionID: sessionID}
}

// SessionID returns the session this stream reports on.
func (s *SessionStream) SessionID() SessionID { return s.sessionID }

// WithMeta returns a stream on the same session whose notifications carry
// meta as their _meta, for tracing or other extension data:
//
//	traced := stream.WithMeta(meta)
//	traced.SendText(ctx, …)
//
// The stream keeps meta, so do not modify it afterwards.
func (s *SessionStream) WithMeta(meta Meta) *SessionStream {
	return &SessionStream{client: s.client, sessionID: s.sessionID, meta: meta}
}

// SendText appends text to the agent message with the given id.
func (s *SessionStream) SendText(ctx context.Context, id MessageID, text string) error {
	return s.SendContent(ctx, id, TextBlock(text))
}

// SendContent appends a content block to the agent message with the given id,
// for images, audio and embedded resources.
func (s *SessionStream) SendContent(ctx context.Context, id MessageID, content ContentBlock) error {
	return s.Send(ctx, schema.SessionUpdateAgentMessageChunk{MessageID: id, Content: content})
}

// SendThought appends text to the agent's reasoning with the given id, which
// clients display separately from its messages.
func (s *SessionStream) SendThought(ctx context.Context, id MessageID, text string) error {
	return s.Send(ctx, schema.SessionUpdateAgentThoughtChunk{MessageID: id, Content: TextBlock(text)})
}

// SendUserMessage reports a user message in full: the message a prompt
// inserted, which v2 agents must echo, or history replayed on resume.
func (s *SessionStream) SendUserMessage(ctx context.Context, id MessageID, content ...ContentBlock) error {
	return s.Send(ctx, schema.SessionUpdateUserMessage{MessageID: id, Content: optional.Of(content)})
}

// StartToolCall reports a tool call that is now running.
func (s *SessionStream) StartToolCall(ctx context.Context, id ToolCallID, title string, kind ToolKind, opts ...ToolCallOption) error {
	return s.toolCall(ctx, id, title, kind, schema.ToolCallStatusInProgress, opts)
}

// ProposeToolCall reports a tool call that has not started running: its input
// is still streaming in, or it waits for the user's permission. Move it on
// with [SessionStream.UpdateToolCallStatus] once it runs, or end it with
// [SessionStream.CompleteToolCall] or [SessionStream.FailToolCall].
func (s *SessionStream) ProposeToolCall(ctx context.Context, id ToolCallID, title string, kind ToolKind, opts ...ToolCallOption) error {
	return s.toolCall(ctx, id, title, kind, schema.ToolCallStatusPending, opts)
}

func (s *SessionStream) toolCall(ctx context.Context, id ToolCallID, title string, kind ToolKind, status ToolCallStatus, opts []ToolCallOption) error {
	update := toolCallUpdate(id, status, opts)
	update.Title, update.Kind = optional.Of(title), optional.Of(kind)
	return s.Send(ctx, update)
}

// UpdateToolCallStatus moves a tool call to another status.
func (s *SessionStream) UpdateToolCallStatus(ctx context.Context, id ToolCallID, status ToolCallStatus, opts ...ToolCallOption) error {
	return s.Send(ctx, toolCallUpdate(id, status, opts))
}

// SendToolOutput appends output to a running tool call.
func (s *SessionStream) SendToolOutput(ctx context.Context, id ToolCallID, content ToolCallContent) error {
	return s.Send(ctx, schema.SessionUpdateToolCallContentChunk{ToolCallID: id, Content: content})
}

// CompleteToolCall marks a tool call completed; [WithToolContent] replaces
// its content with the output.
func (s *SessionStream) CompleteToolCall(ctx context.Context, id ToolCallID, opts ...ToolCallOption) error {
	return s.UpdateToolCallStatus(ctx, id, schema.ToolCallStatusCompleted, opts...)
}

// FailToolCall marks a tool call failed; [WithToolContent] replaces its
// content with the error output.
func (s *SessionStream) FailToolCall(ctx context.Context, id ToolCallID, opts ...ToolCallOption) error {
	return s.UpdateToolCallStatus(ctx, id, schema.ToolCallStatusFailed, opts...)
}

// SendPlan reports the entries of the plan identified by id, creating or
// replacing it; remove a plan with [SessionUpdatePlanRemoved].
func (s *SessionStream) SendPlan(ctx context.Context, id PlanID, entries []PlanEntry) error {
	return s.Send(ctx, schema.SessionUpdatePlanUpdate{
		Plan: NewPlanUpdateContent(PlanUpdateContentItems{PlanID: id, Entries: entries}),
	})
}

// SendCommands reports the slash commands available in this session,
// replacing the list reported before; an empty list clears it.
func (s *SessionStream) SendCommands(ctx context.Context, commands []AvailableCommand) error {
	return s.Send(ctx, schema.SessionUpdateAvailableCommandsUpdate{AvailableCommands: commands})
}

// SendConfigUpdate reports new values for the session's config options.
func (s *SessionStream) SendConfigUpdate(ctx context.Context, options []SessionConfigOption) error {
	return s.Send(ctx, schema.SessionUpdateConfigOptionUpdate{ConfigOptions: options})
}

// SendUsage reports context window usage: used tokens out of size, with an
// optional running cost.
func (s *SessionStream) SendUsage(ctx context.Context, used, size uint64, cost *Cost) error {
	return s.Send(ctx, schema.SessionUpdateUsageUpdate{Used: used, Size: size, Cost: cost})
}

// Running reports that foreground work started or resumed.
func (s *SessionStream) Running(ctx context.Context) error {
	return s.state(ctx, schema.StateUpdateRunning{})
}

// RequiresAction reports that foreground work is blocked on the user, such as
// a pending permission request.
func (s *SessionStream) RequiresAction(ctx context.Context) error {
	return s.state(ctx, schema.StateUpdateRequiresAction{})
}

// Unknown reports that the agent cannot currently tell whether foreground
// work is in progress, such as after losing a remote worker's event feed. It
// does not end the turn; a later state replaces it.
func (s *SessionStream) Unknown(ctx context.Context) error {
	return s.state(ctx, schema.StateUpdateUnknown{})
}

// Idle reports that the agent is ready for a new prompt, ending the turn with
// the given reason. Clients treat this as the end of the turn.
func (s *SessionStream) Idle(ctx context.Context, reason StopReason) error {
	return s.state(ctx, schema.StateUpdateIdle{StopReason: &reason})
}

// RequestPermission asks the user whether the action title describes may go
// ahead, offering options, or [DefaultPermissionOptions] when there are none,
// and returns the option the user chose and whether it allows the action.
// subject says what the request is about, such as a tool call, and may be
// the zero value. A request whose turn was cancelled chooses and allows
// nothing:
//
//	_, allowed, err := stream.RequestPermission(ctx, "Write config.json", acp2.RequestPermissionSubject{})
func (s *SessionStream) RequestPermission(ctx context.Context, title string, subject RequestPermissionSubject, options ...PermissionOption) (choice PermissionOption, allowed bool, err error) {
	if len(options) == 0 {
		options = DefaultPermissionOptions()
	}
	response, err := s.client.RequestPermission(ctx, &RequestPermissionRequest{
		SessionID: s.sessionID,
		Title:     title,
		Subject:   subject,
		Options:   options,
		Meta:      s.meta,
	})
	if err != nil {
		return PermissionOption{}, false, err
	}
	return chosen(options, response)
}

// Send sends any session update variant, including those without a helper:
//
//	stream.Send(ctx, acp2.SessionUpdatePlanRemoved{PlanID: id})
func (s *SessionStream) Send[T schema.SessionUpdateVariants](ctx context.Context, update T) error {
	return s.client.SessionUpdate(ctx, &UpdateSessionNotification{
		SessionID: s.sessionID,
		Update:    schema.NewSessionUpdate(update),
		Meta:      s.meta,
	})
}

func (s *SessionStream) state[T schema.StateUpdateVariants](ctx context.Context, v T) error {
	return s.Send(ctx, schema.SessionUpdateStateUpdate{Value: schema.NewStateUpdate(v)})
}
