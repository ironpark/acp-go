package main

import (
	"context"
	"slices"
	"strings"
	"testing"

	acp "github.com/ironpark/acp-go"
	"github.com/ironpark/acp-go/acp1"
	"github.com/ironpark/acp-go/acp1/acp1test"
)

// start connects a test client to the agent and opens a session in dir.
func start(t *testing.T, client *acp1test.Client) (*acp1.ClientSideConnection, acp1.SessionID) {
	t.Helper()
	agent := acp1test.Connect(t, newAgent(newManager()), client)
	if _, err := agent.Initialize(t.Context(), &acp1.InitializeRequest{ProtocolVersion: acp1.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	created, err := agent.NewSession(t.Context(), &acp1.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return agent, created.SessionID
}

func prompt(t *testing.T, agent *acp1.ClientSideConnection, id acp1.SessionID) acp1.StopReason {
	t.Helper()
	response, err := agent.Prompt(t.Context(), &acp1.PromptRequest{SessionID: id, Prompt: []acp1.ContentBlock{acp1.TextBlock("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	return response.StopReason
}

// editStatuses returns the statuses the edit tool call went through.
func editStatuses(client *acp1test.Client) []acp1.ToolCallStatus {
	var id acp1.ToolCallID
	var statuses []acp1.ToolCallStatus
	for _, n := range client.Updates() {
		switch update := n.Update.Variant().(type) {
		case acp1.SessionUpdateToolCall:
			if update.GetKind() == acp1.ToolKindEdit {
				id = update.ToolCallID
				statuses = append(statuses, update.GetStatus())
			}
		case acp1.SessionUpdateToolCallUpdate:
			if update.ToolCallID == id && update.Status != nil {
				statuses = append(statuses, *update.Status)
			}
		}
	}
	return statuses
}

// TestAskModeAppliesAnAllowedEdit: the edit is proposed, allowed and then
// run, so its tool call moves forward only.
func TestAskModeAppliesAnAllowedEdit(t *testing.T) {
	client := &acp1test.Client{}
	agent, id := start(t, client)
	if reason := prompt(t, agent, id); reason != acp1.StopReasonEndTurn {
		t.Fatalf("stop reason %s", reason)
	}
	if n := len(client.Permissions()); n != 1 {
		t.Fatalf("%d permission requests, want 1", n)
	}
	want := []acp1.ToolCallStatus{acp1.ToolCallStatusPending, acp1.ToolCallStatusInProgress, acp1.ToolCallStatusCompleted}
	if got := editStatuses(client); !slices.Equal(got, want) {
		t.Errorf("edit statuses %v, want %v", got, want)
	}
	if text := client.Text(id); !strings.Contains(text, "Configuration updated.") {
		t.Errorf("text %q", text)
	}
}

// TestAskModeSkipsARejectedEdit: a rejected edit fails without running.
func TestAskModeSkipsARejectedEdit(t *testing.T) {
	client := &acp1test.Client{Permission: acp1test.Reject}
	agent, id := start(t, client)
	prompt(t, agent, id)
	want := []acp1.ToolCallStatus{acp1.ToolCallStatusPending, acp1.ToolCallStatusFailed}
	if got := editStatuses(client); !slices.Equal(got, want) {
		t.Errorf("edit statuses %v, want %v", got, want)
	}
	if text := client.Text(id); !strings.Contains(text, "Skipping") {
		t.Errorf("text %q", text)
	}
}

// TestAutoModeDoesNotAsk: after switching to auto mode the edit needs no
// permission.
func TestAutoModeDoesNotAsk(t *testing.T) {
	client := &acp1test.Client{}
	agent, id := start(t, client)
	if _, err := agent.SetSessionMode(t.Context(), &acp1.SetSessionModeRequest{SessionID: id, ModeID: autoMode}); err != nil {
		t.Fatal(err)
	}
	prompt(t, agent, id)
	if n := len(client.Permissions()); n != 0 {
		t.Errorf("%d permission requests in auto mode", n)
	}
}

func TestUnknownModeIsInvalidParams(t *testing.T) {
	agent, id := start(t, &acp1test.Client{})
	_, err := agent.SetSessionMode(t.Context(), &acp1.SetSessionModeRequest{SessionID: id, ModeID: "yolo"})
	if !acp.IsCode(err, acp.ErrorCodeInvalidParams) {
		t.Errorf("err %v, want invalid params", err)
	}
}

func TestPingExtension(t *testing.T) {
	agent, _ := start(t, &acp1test.Client{})
	result, err := acp.CallExt[pingResult](context.Background(), agent, pingMethod, pingParams{Message: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Reply != "pong: hello" {
		t.Errorf("reply %q", result.Reply)
	}
}
