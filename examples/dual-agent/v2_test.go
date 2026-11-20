package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/ironpark/acp-go"
	"github.com/ironpark/acp-go/acp2"
	"github.com/ironpark/acp-go/acp2/acp2test"
)

// TestV2AnswersPromptsThatJoinATurn: prompts sent at once, some joining the
// turn another started, are all answered.
func TestV2AnswersPromptsThatJoinATurn(t *testing.T) {
	client := &acp2test.Client{}
	agent := acp2test.Connect(t, func(c *acp2.AgentSideConnection) acp2.Agent { return newV2Agent(c) }, client)
	if _, err := agent.Initialize(t.Context(), &acp2.InitializeRequest{ProtocolVersion: acp2.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	created, err := agent.NewSession(t.Context(), &acp2.NewSessionRequest{Cwd: "/"})
	if err != nil {
		t.Fatal(err)
	}
	id := created.SessionID

	const n = 20
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			prompt := []acp2.ContentBlock{acp2.TextBlock(fmt.Sprintf("p%d;", i))}
			if _, err := agent.Prompt(t.Context(), &acp2.PromptRequest{SessionID: id, Prompt: prompt}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	answered := func(*acp2.UpdateSessionNotification) bool {
		return strings.Count(client.Text(id), "v2 echo: ") == n
	}
	if _, err := client.WaitFor(ctx, answered); err != nil {
		t.Fatalf("answered %d of %d prompts: %q", strings.Count(client.Text(id), "v2 echo: "), n, client.Text(id))
	}
}

func TestV2RejectsUnsupportedSystemPrompt(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	client := &acp2test.Client{}
	agent := acp2test.Connect(t, func(c *acp2.AgentSideConnection) acp2.Agent { return newV2Agent(c) }, client)
	if _, err := agent.Initialize(ctx, &acp2.InitializeRequest{ProtocolVersion: acp2.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	created, err := agent.NewSession(ctx, &acp2.NewSessionRequest{Cwd: "/"})
	if err != nil {
		t.Fatal(err)
	}
	sp := &acp2.SystemPrompt{
		Mode:    acp2.SystemPromptModeAppend,
		Content: []acp2.ContentBlock{acp2.TextBlock("instructions")},
	}
	if _, err := agent.NewSession(ctx, &acp2.NewSessionRequest{Cwd: "/", SystemPrompt: sp}); !acp.IsCode(err, acp.ErrorCodeInvalidParams) {
		t.Fatalf("session/new with unsupported systemPrompt: got %v, want -32602", err)
	}
	listed, err := agent.ListSessions(ctx, &acp2.ListSessionsRequest{})
	if err != nil || len(listed.Sessions) != 1 || listed.Sessions[0].SessionID != created.SessionID {
		t.Fatalf("sessions after rejected session/new = %+v, %v", listed, err)
	}
	before := len(client.Updates())
	if _, err := agent.ResumeSession(ctx, &acp2.ResumeSessionRequest{
		SessionID:    created.SessionID,
		Cwd:          "/",
		ReplayFrom:   acp2.NewReplayFrom(acp2.ReplayFromStart{}),
		SystemPrompt: sp,
	}); !acp.IsCode(err, acp.ErrorCodeInvalidParams) {
		t.Fatalf("session/resume with unsupported systemPrompt: got %v, want -32602", err)
	}
	if len(client.Updates()) != before {
		t.Fatalf("updates sent during rejected session/resume: %+v", client.Updates()[before:])
	}
}

func TestV2ResumeSendsReplayCompleteAfterHistory(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	client := &acp2test.Client{}
	agent := acp2test.Connect(t, func(c *acp2.AgentSideConnection) acp2.Agent { return newV2Agent(c) }, client)
	if _, err := agent.Initialize(ctx, &acp2.InitializeRequest{ProtocolVersion: acp2.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	session, err := agent.StartSession(ctx, &acp2.NewSessionRequest{Cwd: "/"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Prompt(ctx, acp2.TextBlock("hi")); err != nil {
		t.Fatal(err)
	}
	if _, err := session.UntilIdle(ctx).Wait(); err != nil {
		t.Fatal(err)
	}
	before := len(client.Updates())
	if _, err := agent.ResumeSession(ctx, &acp2.ResumeSessionRequest{
		SessionID:  session.ID,
		Cwd:        "/",
		ReplayFrom: acp2.NewReplayFrom(acp2.ReplayFromStart{}),
	}); err != nil {
		t.Fatal(err)
	}
	replayed := client.Updates()[before:]
	if len(replayed) != 3 ||
		replayed[0].Update.Tag() != "user_message" ||
		replayed[1].Update.Tag() != "agent_message_chunk" ||
		replayed[2].Update.Tag() != "replay_complete" {
		t.Fatalf("replayed updates = %+v, want [user_message agent_message_chunk replay_complete]", replayed)
	}
}
