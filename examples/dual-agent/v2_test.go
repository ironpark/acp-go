package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

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
