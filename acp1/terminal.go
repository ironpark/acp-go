package acp1

import (
	"context"
	"time"
)

// TerminalHandle binds a terminal id to its session so an agent can poll,
// wait, kill and release without repeating both ids.
//
// Always Release a terminal when done; the client keeps the process and its
// buffered output alive until then.
//
// See protocol docs: [Terminals](https://agentclientprotocol.com/protocol/terminals)
type TerminalHandle struct {
	ID        TerminalID
	sessionID SessionID
	client    TerminalHandler
}

// NewTerminalHandle binds an existing terminal id to the client that owns it:
// an [AgentSideConnection], or any [TerminalHandler], such as a fake in a
// test. [AgentSideConnection.NewTerminal] creates one directly.
func NewTerminalHandle(id TerminalID, sessionID SessionID, client TerminalHandler) *TerminalHandle {
	return &TerminalHandle{ID: id, sessionID: sessionID, client: client}
}

// CurrentOutput returns the output so far without waiting for exit.
func (t *TerminalHandle) CurrentOutput(ctx context.Context) (*TerminalOutputResponse, error) {
	return t.client.TerminalOutput(ctx, &TerminalOutputRequest{
		SessionID:  t.sessionID,
		TerminalID: t.ID,
	})
}

// WaitForExit blocks until the command exits and reports its status.
func (t *TerminalHandle) WaitForExit(ctx context.Context) (*WaitForTerminalExitResponse, error) {
	return t.client.WaitForTerminalExit(ctx, &WaitForTerminalExitRequest{
		SessionID:  t.sessionID,
		TerminalID: t.ID,
	})
}

// Kill stops the command but keeps the terminal id valid, so the final output
// and exit status remain readable.
func (t *TerminalHandle) Kill(ctx context.Context) error {
	_, err := t.client.KillTerminal(ctx, &KillTerminalRequest{
		SessionID:  t.sessionID,
		TerminalID: t.ID,
	})
	return err
}

// Release kills the command if it is still running and frees the terminal.
// The id is invalid afterwards, though tool calls that already reference it
// keep displaying its output.
func (t *TerminalHandle) Release(ctx context.Context) error {
	_, err := t.client.ReleaseTerminal(ctx, &ReleaseTerminalRequest{
		SessionID:  t.sessionID,
		TerminalID: t.ID,
	})
	return err
}

// TerminalRun is how a command run with [SessionStream.RunTerminal] ended.
type TerminalRun struct {
	// TerminalID is the released terminal, which tool call content can
	// still show with [ToolTerminal].
	TerminalID TerminalID
	// Output is what the command printed, as the client retained it.
	Output string
	// Truncated reports that the client dropped the start of the output to
	// stay within the request's OutputByteLimit.
	Truncated bool
	// ExitStatus is how the command exited, as the client reports it; it
	// may be nil for a command that was killed.
	ExitStatus *TerminalExitStatus
	// TimedOut reports that the command ran past the timeout and was killed.
	TimedOut bool
}

// RunTerminal runs a command in a new terminal of the stream's session and
// waits for it to exit, then returns its output and exit status and releases
// the terminal. When toolCallID is not empty, the tool call is moved to in
// progress with the terminal as its content first, so the client shows the
// output live and keeps showing it after the release.
//
// A timeout greater than zero bounds the wait: the command is then killed and
// the run reports TimedOut. When ctx is cancelled, as by the turn's
// cancellation, the command is killed too, and RunTerminal returns what it
// printed along with ctx's error. The kill, the final output and the release
// are requested even then.
//
//	run, err := stream.RunTerminal(ctx, toolID, acp1.CreateTerminalRequest{
//		Command: "go", Args: []string{"test", "./..."},
//	}, 2*time.Minute)
func (s *SessionStream) RunTerminal(ctx context.Context, toolCallID ToolCallID, params CreateTerminalRequest, timeout time.Duration) (*TerminalRun, error) {
	terminal, err := s.NewTerminal(ctx, params)
	if err != nil {
		return nil, err
	}
	detached := context.WithoutCancel(ctx)
	defer terminal.Release(detached)
	if toolCallID != "" {
		if err := s.UpdateToolCallStatus(ctx, toolCallID, ToolCallStatusInProgress,
			WithToolContent(ToolTerminal(terminal.ID))); err != nil {
			return nil, err
		}
	}

	wait := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		wait, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	run := &TerminalRun{TerminalID: terminal.ID}
	exit, err := terminal.WaitForExit(wait)
	if err != nil {
		if wait.Err() == nil {
			return nil, err // the client failed the wait
		}
		run.TimedOut = ctx.Err() == nil
		if err := terminal.Kill(detached); err != nil {
			return nil, err
		}
	}
	output, err := terminal.CurrentOutput(detached)
	if err != nil {
		return nil, err
	}
	run.Output, run.Truncated, run.ExitStatus = output.Output, output.Truncated, output.ExitStatus
	if run.ExitStatus == nil && exit != nil {
		run.ExitStatus = &TerminalExitStatus{ExitCode: exit.ExitCode, Signal: exit.Signal}
	}
	return run, ctx.Err()
}
