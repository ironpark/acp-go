package acp1_test

import (
	"errors"
	"testing"

	"github.com/ironpark/acp-go/acp1"
	"github.com/ironpark/acp-go/acp1/acp1test"
)

func TestSubagentStream(t *testing.T) {
	client := newTestClient()
	parent := acp1.NewSessionStream(client, "parent")
	ctx := t.Context()

	sub, err := parent.StartSubagent(ctx, "child", "Run the tests", acp1.WithSubagentCancel())
	if err != nil {
		t.Fatal(err)
	}
	announced := <-client.updates
	update, ok := announced.Update.As[acp1.SessionUpdateSubagentUpdate]()
	if !ok || announced.SessionID != "parent" || update.SessionID != "child" || update.GetTitle() != "Run the tests" ||
		update.GetCapabilities().GetCancel() == nil {
		t.Fatalf("announcement = %+v", announced)
	}

	// The child's own updates go out under its id.
	if err := sub.SendText(ctx, "working"); err != nil {
		t.Fatal(err)
	}
	if got := <-client.updates; got.SessionID != "child" {
		t.Fatalf("child text went to %q", got.SessionID)
	}

	// Its state is reported on the parent.
	if err := sub.Idle(ctx, acp1.StopReasonCancelled); err != nil {
		t.Fatal(err)
	}
	got := <-client.updates
	update, _ = got.Update.As[acp1.SessionUpdateSubagentUpdate]()
	idle, ok := update.GetState().As[acp1.StateUpdateIdle]()
	if got.SessionID != "parent" || update.SessionID != "child" || !ok || idle.GetStopReason() != acp1.StopReasonCancelled {
		t.Fatalf("state update = %+v", got)
	}
}

func TestSubagentNeedsTheClientCapability(t *testing.T) {
	var agentConn *acp1.AgentSideConnection
	conn := acp1test.Connect(t, func(c *acp1.AgentSideConnection) acp1.Agent {
		agentConn = c
		return newHelperAgent(acp1.NewMemoryStore[commandSession]())
	}, &acp1test.Client{})
	ctx := t.Context()
	if _, err := conn.Initialize(ctx, &acp1.InitializeRequest{ProtocolVersion: acp1.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	if _, err := acp1.NewSessionStream(agentConn, "s1").StartSubagent(ctx, "child", "x"); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("StartSubagent = %v, want errors.ErrUnsupported", err)
	}
}
