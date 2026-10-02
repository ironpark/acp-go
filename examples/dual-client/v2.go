package main

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/ironpark/acp-go/acp2"
)

// v2Client prints the history a session/resume replays; each Turn collects
// its own updates. The agent never asks for permission.
type v2Client struct {
	acp2.UnimplementedClient
	replaying atomic.Bool
}

func (c *v2Client) SessionUpdate(_ context.Context, params *acp2.UpdateSessionNotification) error {
	if !c.replaying.Load() {
		return nil
	}
	switch update := params.Update.Variant().(type) {
	case acp2.SessionUpdateUserMessage:
		fmt.Printf("   history >> %s\n", acp2.JoinTexts(update.GetContent()))
	case acp2.SessionUpdateAgentMessageChunk:
		if text, ok := acp2.TextOf(update.Content); ok {
			fmt.Printf("   history << %s\n", text)
		}
	}
	return nil
}

// promptV2 runs one turn in a new session: the prompt response only accepts
// the message, and the turn ends with the idle update's stop reason. It then
// closes the session and resumes it with a replay of its history, the v2
// replacement for session/load, and prompts again.
func promptV2(ctx context.Context, agent *acp2.RemoteAgent, client *v2Client, cwd, prompt string) error {
	session, err := agent.StartSession(ctx, &acp2.NewSessionRequest{Cwd: acp2.AbsolutePath(cwd)})
	if err != nil {
		return err
	}
	if err := turnV2(ctx, session, prompt); err != nil {
		return err
	}

	// Closing ends the work on the session; the agent keeps it to resume.
	if _, err := agent.CloseSession(ctx, &acp2.CloseSessionRequest{SessionID: session.ID}); err != nil {
		return fmt.Errorf("close session: %w", err)
	}
	fmt.Println("closed the session; resuming it")
	// The replay arrives as session updates before ResumeSession returns.
	client.replaying.Store(true)
	_, err = agent.ResumeSession(ctx, &acp2.ResumeSessionRequest{
		SessionID:  session.ID,
		Cwd:        acp2.AbsolutePath(cwd),
		ReplayFrom: acp2.NewReplayFrom(acp2.ReplayFromStart{}),
	})
	client.replaying.Store(false)
	if err != nil {
		return fmt.Errorf("resume session: %w", err)
	}
	return turnV2(ctx, session, prompt+" again")
}

func turnV2(ctx context.Context, session *acp2.ClientSession, prompt string) error {
	turn, _, err := session.Prompt(ctx, acp2.TextBlock(prompt))
	if err != nil {
		return err
	}
	text, reason, err := turn.Text()
	if err != nil {
		return err
	}
	fmt.Printf("<< %s\nstop reason: %s\n", text, reason)
	return nil
}
