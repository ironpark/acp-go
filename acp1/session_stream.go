package acp1

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"

	schema "github.com/ironpark/acp-go/schema/v1"
)

// SendOption sets optional fields on an outgoing session update.
type SendOption func(*sendOptions)

type sendOptions struct {
	messageID *MessageID
}

// WithMessageID groups consecutive chunks into one logical message.
func WithMessageID(id MessageID) SendOption {
	return func(o *sendOptions) { o.messageID = &id }
}

func applySendOptions(opts []SendOption) sendOptions {
	var o sendOptions
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

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

func applyToolCallOptions(opts []ToolCallOption) toolCallOptions {
	var o toolCallOptions
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// SessionStream sends session/update notifications for one session.
//
// It removes the boilerplate of naming the session and building a
// [SessionUpdate] variant for each chunk:
//
//	stream := acp1.NewSessionStream(client, sessionID)
//	stream.SendText(ctx, "Reading the file…")
//	stream.StartToolCall(ctx, toolID, "Read file", acp1.ToolKindRead, acp1.WithLocations(location))
//	stream.CompleteToolCall(ctx, toolID, acp1.WithToolContent(acp1.ToolText(contents)))
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

// SendText streams agent message text.
func (s *SessionStream) SendText(ctx context.Context, text string, opts ...SendOption) error {
	o := applySendOptions(opts)
	return s.Send(ctx, schema.SessionUpdateAgentMessageChunk{
		Content:   TextBlock(text),
		MessageID: o.messageID,
	})
}

// SendThought streams the agent's reasoning, which clients display separately
// from its message.
func (s *SessionStream) SendThought(ctx context.Context, text string, opts ...SendOption) error {
	o := applySendOptions(opts)
	return s.Send(ctx, schema.SessionUpdateAgentThoughtChunk{
		Content:   TextBlock(text),
		MessageID: o.messageID,
	})
}

// SendUserMessage echoes user message text, which agents use when replaying a
// loaded session's history.
func (s *SessionStream) SendUserMessage(ctx context.Context, text string, opts ...SendOption) error {
	return s.SendUserContent(ctx, TextBlock(text), opts...)
}

// SendUserContent replays an arbitrary content block of the user's message,
// such as an image, while loading a session.
func (s *SessionStream) SendUserContent(ctx context.Context, content ContentBlock, opts ...SendOption) error {
	o := applySendOptions(opts)
	return s.Send(ctx, schema.SessionUpdateUserMessageChunk{
		Content:   content,
		MessageID: o.messageID,
	})
}

// SendContent streams an arbitrary content block as an agent message chunk,
// for images, audio and embedded resources.
func (s *SessionStream) SendContent(ctx context.Context, content ContentBlock, opts ...SendOption) error {
	o := applySendOptions(opts)
	return s.Send(ctx, schema.SessionUpdateAgentMessageChunk{
		Content:   content,
		MessageID: o.messageID,
	})
}

// StartToolCall reports a tool call that is now running.
func (s *SessionStream) StartToolCall(ctx context.Context, id ToolCallID, title string, kind ToolKind, opts ...ToolCallOption) error {
	return s.toolCall(ctx, id, title, kind, schema.ToolCallStatusInProgress, opts)
}

// ProposeToolCall reports a tool call that has not started running: its input
// is still streaming in, or it waits for the user's permission. Move it on
// with [SessionStream.UpdateToolCallStatus] once it runs, or end it with
// [SessionStream.CompleteToolCall] or [SessionStream.FailToolCall]:
//
//	stream.ProposeToolCall(ctx, id, "Run tests", acp1.ToolKindExecute)
//	if _, allowed, err := stream.RequestPermission(ctx, acp1.ToolCallUpdate{ToolCallID: id}); err != nil || !allowed {
//		return stream.FailToolCall(ctx, id)
//	}
//	stream.UpdateToolCallStatus(ctx, id, acp1.ToolCallStatusInProgress)
func (s *SessionStream) ProposeToolCall(ctx context.Context, id ToolCallID, title string, kind ToolKind, opts ...ToolCallOption) error {
	return s.toolCall(ctx, id, title, kind, schema.ToolCallStatusPending, opts)
}

func (s *SessionStream) toolCall(ctx context.Context, id ToolCallID, title string, kind ToolKind, status ToolCallStatus, opts []ToolCallOption) error {
	o := applyToolCallOptions(opts)
	return s.Send(ctx, schema.SessionUpdateToolCall{
		ToolCallID: id,
		Title:      title,
		Kind:       &kind,
		Status:     &status,
		Content:    o.content,
		Locations:  o.locations,
		RawInput:   o.rawInput,
		RawOutput:  o.rawOutput,
	})
}

// UpdateToolCallStatus moves a tool call to another status.
func (s *SessionStream) UpdateToolCallStatus(ctx context.Context, id ToolCallID, status ToolCallStatus, opts ...ToolCallOption) error {
	o := applyToolCallOptions(opts)
	return s.Send(ctx, schema.SessionUpdateToolCallUpdate{
		ToolCallID: id,
		Status:     &status,
		Content:    o.content,
		Locations:  o.locations,
		RawInput:   o.rawInput,
		RawOutput:  o.rawOutput,
	})
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

// SendPlan reports the agent's plan for the turn.
func (s *SessionStream) SendPlan(ctx context.Context, entries []PlanEntry) error {
	return s.Send(ctx, schema.SessionUpdatePlan{Entries: entries})
}

// SendModeUpdate reports that the agent switched session mode on its own.
func (s *SessionStream) SendModeUpdate(ctx context.Context, modeID SessionModeID) error {
	return s.Send(ctx, schema.SessionUpdateCurrentModeUpdate{CurrentModeID: modeID})
}

// SendConfigUpdate reports new values for the session's config options.
func (s *SessionStream) SendConfigUpdate(ctx context.Context, options []SessionConfigOption) error {
	return s.Send(ctx, schema.SessionUpdateConfigOptionUpdate{ConfigOptions: options})
}

// SendCommands reports the slash commands available in this session.
func (s *SessionStream) SendCommands(ctx context.Context, commands []AvailableCommand) error {
	return s.Send(ctx, schema.SessionUpdateAvailableCommandsUpdate{AvailableCommands: commands})
}

// SendUsage reports context window usage for the turn so far: used tokens out
// of size, with an optional running cost.
func (s *SessionStream) SendUsage(ctx context.Context, used, size uint64, cost *Cost) error {
	return s.Send(ctx, schema.SessionUpdateUsageUpdate{
		Used: used,
		Size: size,
		Cost: cost,
	})
}

// RequestPermission asks the user whether the tool call may go ahead, offering
// options, or [DefaultPermissionOptions] when there are none, and returns the
// option the user chose and whether it allows the call. A request whose turn
// was cancelled chooses and allows nothing:
//
//	_, allowed, err := stream.RequestPermission(ctx, acp1.ToolCallUpdate{
//		ToolCallID: id,
//		Status:     new(acp1.ToolCallStatusPending),
//		Content:    []acp1.ToolCallContent{diff},
//	})
func (s *SessionStream) RequestPermission(ctx context.Context, toolCall ToolCallUpdate, options ...PermissionOption) (choice PermissionOption, allowed bool, err error) {
	if len(options) == 0 {
		options = DefaultPermissionOptions()
	}
	response, err := s.client.RequestPermission(ctx, &RequestPermissionRequest{
		SessionID: s.sessionID,
		ToolCall:  toolCall,
		Options:   options,
		Meta:      s.meta,
	})
	if err != nil {
		return PermissionOption{}, false, err
	}
	return chosen(options, response)
}

// ReadTextFile reads a text file through the client, which includes unsaved
// changes in its editor. The stream's client must implement [FileReader], as
// [AgentSideConnection] does; read part of a file with its ReadTextFile. When
// the client did not advertise fs.readTextFile, it fails without a request
// with an error that matches [errors.ErrUnsupported].
func (s *SessionStream) ReadTextFile(ctx context.Context, path string) (string, error) {
	reader, ok := s.client.(FileReader)
	if !ok {
		return "", errors.New("acp1: the stream's client cannot read files")
	}
	if err := s.require("fs.readTextFile", func(c *ClientCapabilities) bool { return c.GetFS().GetReadTextFile() }); err != nil {
		return "", err
	}
	response, err := reader.ReadTextFile(ctx, &ReadTextFileRequest{SessionID: s.sessionID, Path: path, Meta: s.meta})
	if err != nil {
		return "", err
	}
	return response.Content, nil
}

// WriteTextFile writes a text file through the client. The stream's client
// must implement [FileWriter], as [AgentSideConnection] does. When the client
// did not advertise fs.writeTextFile, it fails without a request with an
// error that matches [errors.ErrUnsupported].
func (s *SessionStream) WriteTextFile(ctx context.Context, path, content string) error {
	writer, ok := s.client.(FileWriter)
	if !ok {
		return errors.New("acp1: the stream's client cannot write files")
	}
	if err := s.require("fs.writeTextFile", func(c *ClientCapabilities) bool { return c.GetFS().GetWriteTextFile() }); err != nil {
		return err
	}
	_, err := writer.WriteTextFile(ctx, &WriteTextFileRequest{SessionID: s.sessionID, Path: path, Content: content, Meta: s.meta})
	return err
}

// NewTerminal creates a terminal in the stream's session, which it sets as
// params' SessionID, and returns a handle bound to it. The stream's client
// must implement [TerminalHandler], as [AgentSideConnection] does. When the
// client did not advertise terminal, it fails without a request with an
// error that matches [errors.ErrUnsupported].
func (s *SessionStream) NewTerminal(ctx context.Context, params CreateTerminalRequest) (*TerminalHandle, error) {
	terminals, ok := s.client.(TerminalHandler)
	if !ok {
		return nil, errors.New("acp1: the stream's client cannot run terminals")
	}
	if err := s.require("terminal", func(c *ClientCapabilities) bool { return c.GetTerminal() }); err != nil {
		return nil, err
	}
	params.SessionID = s.sessionID
	return newTerminal(ctx, terminals, &params)
}

// Send sends any session update variant, including those without a helper:
//
//	stream.Send(ctx, acp1.SessionUpdatePlan{Entries: entries})
func (s *SessionStream) Send[T schema.SessionUpdateVariants](ctx context.Context, update T) error {
	return s.client.SessionUpdate(ctx, &SessionNotification{
		SessionID: s.sessionID,
		Update:    schema.NewSessionUpdate(update),
		Meta:      s.meta,
	})
}

// capabilityReporter is a client that knows the capabilities its peer
// advertised, as [AgentSideConnection] does once initialized.
type capabilityReporter interface {
	ClientCapabilities() *ClientCapabilities
}

// require fails with [errors.ErrUnsupported] when the stream's client knows
// the peer's capabilities and has reports the named one missing. A client
// that does not know them, such as a connection not yet initialized or a fake
// in a test, is assumed to support it.
func (s *SessionStream) require(name string, has func(*ClientCapabilities) bool) error {
	reporter, ok := s.client.(capabilityReporter)
	if !ok {
		return nil
	}
	if caps := reporter.ClientCapabilities(); caps != nil && !has(caps) {
		return fmt.Errorf("acp1: the client does not support %s: %w", name, errors.ErrUnsupported)
	}
	return nil
}
