package acp2_test

import (
	"testing"

	"github.com/ironpark/acp-go/acp2"
)

func TestSubagentStreamMirrorsState(t *testing.T) {
	client := newTestClient()
	parent := acp2.NewSessionStream(client, "parent")
	ctx := t.Context()

	sub, err := parent.StartSubagent(ctx, "child", "Run the tests", acp2.WithSubagentDescription("go test ./..."))
	if err != nil {
		t.Fatal(err)
	}
	announced := <-client.updates
	update, ok := announced.Update.As[acp2.SessionUpdateSubagentUpdate]()
	if !ok || announced.SessionID != "parent" || update.SessionID != "child" || update.GetDescription() != "go test ./..." {
		t.Fatalf("announcement = %+v", announced)
	}

	// The child reports its state, and the parent's roster mirrors it.
	if err := sub.Running(ctx); err != nil {
		t.Fatal(err)
	}
	child := <-client.updates
	if state, ok := child.Update.As[acp2.SessionUpdateStateUpdate](); child.SessionID != "child" || !ok || state.Value.Tag() != "running" {
		t.Fatalf("child state = %+v", child)
	}
	mirrored := <-client.updates
	update, _ = mirrored.Update.As[acp2.SessionUpdateSubagentUpdate]()
	if mirrored.SessionID != "parent" || update.SessionID != "child" || update.GetState().Tag() != "running" {
		t.Fatalf("mirrored state = %+v", mirrored)
	}

	if err := sub.Unknown(ctx); err != nil {
		t.Fatal(err)
	}
	if child := <-client.updates; child.SessionID != "child" {
		t.Fatalf("unknown went to %q", child.SessionID)
	}
	<-client.updates
}
