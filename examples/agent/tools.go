package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/ironpark/acp-go/acp1"
)

// runTurn plans the turn, then works through it: a command in the client's
// terminal, a file read, and a file edit that needs permission in ask mode.
// Each plan update replaces the last, so the agent resends the whole plan as
// each step finishes and the next starts.
//
// A tool call id must be unique within the session, across turns, so each
// tool call gets a generated one.
func (a *exampleAgent) runTurn(ctx context.Context, sessionID acp1.SessionID, sess *session, prompt string) error {
	stream := acp1.NewSessionStream(a.client, sessionID)
	plan := []acp1.PlanEntry{
		{Content: "Check the Go toolchain", Priority: acp1.PlanEntryPriorityMedium, Status: acp1.PlanEntryStatusInProgress},
		{Content: "Read the project", Priority: acp1.PlanEntryPriorityMedium, Status: acp1.PlanEntryStatusPending},
		{Content: "Update the configuration", Priority: acp1.PlanEntryPriorityHigh, Status: acp1.PlanEntryStatusPending},
	}
	// step runs entry i, then marks it completed and the next in progress.
	step := func(i int, run func() error) error {
		if err := run(); err != nil {
			return err
		}
		plan[i].Status = acp1.PlanEntryStatusCompleted
		if i+1 < len(plan) {
			plan[i+1].Status = acp1.PlanEntryStatusInProgress
		}
		return stream.SendPlan(ctx, plan)
	}

	if err := stream.SendText(ctx, fmt.Sprintf("You said %q. Here is my plan.", prompt)); err != nil {
		return err
	}
	if err := stream.SendPlan(ctx, plan); err != nil {
		return err
	}
	if err := step(0, func() error { return a.runCommand(ctx, stream, sess, "go", "version") }); err != nil {
		return err
	}
	if err := step(1, func() error { return readProject(ctx, stream) }); err != nil {
		return err
	}
	return step(2, func() error { return a.editConfig(ctx, stream, sess) })
}

// runCommand runs a command in a terminal the client owns and shows its
// output in the tool call as it runs. Clients without the terminal
// capability cannot, so the agent says so instead.
func (a *exampleAgent) runCommand(ctx context.Context, stream *acp1.SessionStream, sess *session, command string, args ...string) error {
	id := acp1.GenerateToolCallID()
	title := "Running " + strings.Join(append([]string{command}, args...), " ")
	// The tool call is pending until RunTerminal moves it to in progress with
	// the terminal as its content; the client keeps showing the output once
	// the terminal is released.
	if err := stream.ProposeToolCall(ctx, id, title, acp1.ToolKindExecute); err != nil {
		return err
	}
	run, err := stream.RunTerminal(ctx, id, acp1.CreateTerminalRequest{
		Command: command,
		Args:    args,
		Cwd:     &sess.cwd,
	}, time.Minute)
	switch {
	case errors.Is(err, errors.ErrUnsupported):
		// The client did not advertise the terminal capability.
		return stream.CompleteToolCall(ctx, id, acp1.WithToolContent(acp1.ToolText("This client cannot run commands.")))
	case err != nil && ctx.Err() != nil:
		return err // the turn was cancelled
	case err != nil:
		return stream.FailToolCall(ctx, id, acp1.WithToolContent(acp1.ToolText(err.Error())))
	}
	// No exit code means a signal ended it, so ExitCode is checked for nil
	// rather than read with GetExitCode, which would give 0.
	if run.ExitStatus == nil || run.ExitStatus.ExitCode == nil || *run.ExitStatus.ExitCode != 0 {
		return stream.FailToolCall(ctx, id)
	}
	return stream.CompleteToolCall(ctx, id)
}

func readProject(ctx context.Context, stream *acp1.SessionStream) error {
	id := acp1.GenerateToolCallID()
	if err := stream.StartToolCall(ctx, id, "Reading project files", acp1.ToolKindRead); err != nil {
		return err
	}
	if err := pause(ctx); err != nil {
		return err
	}
	return stream.CompleteToolCall(ctx, id, acp1.WithToolContent(acp1.ToolText("# My Project")))
}

// editConfig proposes a change to config.json and reports it as a diff. In
// ask mode it asks the user first. The example does not write the file.
func (a *exampleAgent) editConfig(ctx context.Context, stream *acp1.SessionStream, sess *session) error {
	id := acp1.GenerateToolCallID()
	path := filepath.Join(sess.cwd, "config.json")
	if err := stream.StartToolCall(ctx, id, "Modifying configuration", acp1.ToolKindEdit, acp1.WithLocations(acp1.ToolCallLocation{Path: path})); err != nil {
		return err
	}
	oldText, newText := "{\"debug\": false}\n", "{\"debug\": true}\n"
	diff := acp1.ToolDiff(path, &oldText, newText)

	if sess.currentMode() == askMode {
		allowed, err := askPermission(ctx, stream, id, path, diff)
		if err != nil {
			return err
		}
		if !allowed {
			if err := stream.FailToolCall(ctx, id); err != nil {
				return err
			}
			return stream.SendText(ctx, " Skipping the configuration update.")
		}
	}
	if err := stream.CompleteToolCall(ctx, id, acp1.WithToolContent(diff)); err != nil {
		return err
	}
	return stream.SendText(ctx, " Configuration updated.")
}

// askPermission shows the user the proposed diff and asks whether to apply it.
func askPermission(ctx context.Context, stream *acp1.SessionStream, id acp1.ToolCallID, path string, diff acp1.ToolCallContent) (bool, error) {
	// Anything but an allowing choice, including a cancelled request, skips
	// the change.
	toolCall := acp1.ToolCallUpdate{
		ToolCallID: id,
		Title:      new("Modifying configuration"),
		Kind:       new(acp1.ToolKindEdit),
		Status:     new(acp1.ToolCallStatusPending),
		Locations:  []acp1.ToolCallLocation{{Path: path}},
		Content:    []acp1.ToolCallContent{diff},
	}
	_, allowed, err := stream.RequestPermission(ctx, toolCall,
		acp1.NewPermissionOption(acp1.PermissionOptionKindAllowOnce, "Allow this change"),
		acp1.NewPermissionOption(acp1.PermissionOptionKindRejectOnce, "Skip this change"))
	return allowed, err
}

// pause stands in for real work and returns early when the turn is cancelled.
func pause(ctx context.Context) error {
	select {
	case <-time.After(500 * time.Millisecond):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
