package acp2

import (
	"context"

	schema "github.com/ironpark/acp-go/schema/v2"
)

// SubagentOption sets optional fields on the subagent_update
// [SessionStream.StartSubagent] and [SubagentStream.Update] send.
type SubagentOption func(*SessionUpdateSubagentUpdate)

// WithSubagentDescription describes the subagent's assignment.
func WithSubagentDescription(description string) SubagentOption {
	return func(u *SessionUpdateSubagentUpdate) { u.Description = &description }
}

// WithSubagentCancel lets the client cancel the subagent's work with
// session/cancel on its session, which the agent must then honor.
func WithSubagentCancel() SubagentOption {
	return func(u *SessionUpdateSubagentUpdate) {
		u.Capabilities = &SubagentSessionCapabilities{Cancel: &SessionCancelCapabilities{}}
	}
}

// SubagentStream is the stream of a child session that a parent session
// exposes as a subagent. Its embedded [SessionStream] reports the child's own
// updates under the child's session id. Its state methods report on the
// child's stream, as any session's do, and mirror the same state on the
// parent's subagent_update, so the parent's roster stays current.
//
// Subagents are an unstable draft of the protocol and may change.
type SubagentStream struct {
	*SessionStream
	parent *SessionStream
}

// StartSubagent announces child as a subagent of this session and returns
// its stream. The announcement goes out before any update of the child, as
// the protocol requires:
//
//	sub, err := stream.StartSubagent(ctx, childID, "Run the tests")
//	sub.Running(ctx)
//	sub.SendText(ctx, messageID, "Running go test ./...")
//	sub.Idle(ctx, acp2.StopReasonEndTurn)
//
// Subagents are part of the v2 session model, so no capability gates them.
func (s *SessionStream) StartSubagent(ctx context.Context, child SessionID, title string, opts ...SubagentOption) (*SubagentStream, error) {
	sub := &SubagentStream{
		SessionStream: &SessionStream{client: s.client, sessionID: child, meta: s.meta},
		parent:        s,
	}
	return sub, sub.Update(ctx, append([]SubagentOption{func(u *SessionUpdateSubagentUpdate) { u.Title = &title }}, opts...)...)
}

// Update reports changes to the subagent's title, description or controls on
// the parent session. Fields opts leave unset stay as they were.
func (s *SubagentStream) Update(ctx context.Context, opts ...SubagentOption) error {
	update := SessionUpdateSubagentUpdate{SessionID: s.sessionID}
	for _, opt := range opts {
		opt(&update)
	}
	return s.parent.Send(ctx, update)
}

// Running reports that the subagent's foreground work started or resumed.
func (s *SubagentStream) Running(ctx context.Context) error {
	return s.mirror(ctx, schema.NewStateUpdate(schema.StateUpdateRunning{}))
}

// RequiresAction reports that the subagent's work is blocked on the user.
func (s *SubagentStream) RequiresAction(ctx context.Context) error {
	return s.mirror(ctx, schema.NewStateUpdate(schema.StateUpdateRequiresAction{}))
}

// Idle reports that the subagent's work stopped for reason; the parent may
// give it more. Cancelled work is idle with [StopReasonCancelled].
func (s *SubagentStream) Idle(ctx context.Context, reason StopReason) error {
	return s.mirror(ctx, schema.NewStateUpdate(schema.StateUpdateIdle{StopReason: &reason}))
}

// Unknown reports that the agent lost sight of the subagent's work, such as
// a remote child's event feed; a later state replaces it.
func (s *SubagentStream) Unknown(ctx context.Context) error {
	return s.mirror(ctx, schema.NewStateUpdate(schema.StateUpdateUnknown{}))
}

// mirror reports state on the child's stream, then the same snapshot on the
// parent's.
func (s *SubagentStream) mirror(ctx context.Context, state StateUpdate) error {
	if err := s.Send(ctx, schema.SessionUpdateStateUpdate{Value: state}); err != nil {
		return err
	}
	return s.parent.Send(ctx, SessionUpdateSubagentUpdate{SessionID: s.sessionID, State: state})
}
