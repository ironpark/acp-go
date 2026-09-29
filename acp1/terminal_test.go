package acp1_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ironpark/acp-go/acp1"
	"github.com/ironpark/acp-go/acp1/acp1test"
)

// terminalClient runs one fake command: it exits with exitCode unless hang is
// set, in which case it runs until killed.
type terminalClient struct {
	acp1test.Client
	hang     bool
	exitCode uint32

	mu    sync.Mutex
	calls []string
}

func (c *terminalClient) record(call string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, call)
}

func (c *terminalClient) CreateTerminal(_ context.Context, params *acp1.CreateTerminalRequest) (*acp1.CreateTerminalResponse, error) {
	c.record("create " + params.Command)
	return &acp1.CreateTerminalResponse{TerminalID: "term_1"}, nil
}

func (c *terminalClient) TerminalOutput(context.Context, *acp1.TerminalOutputRequest) (*acp1.TerminalOutputResponse, error) {
	c.record("output")
	response := &acp1.TerminalOutputResponse{Output: "ok\n"}
	if !c.hang {
		response.ExitStatus = &acp1.TerminalExitStatus{ExitCode: new(c.exitCode)}
	}
	return response, nil
}

func (c *terminalClient) WaitForTerminalExit(ctx context.Context, _ *acp1.WaitForTerminalExitRequest) (*acp1.WaitForTerminalExitResponse, error) {
	c.record("wait")
	if c.hang {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &acp1.WaitForTerminalExitResponse{ExitCode: new(c.exitCode)}, nil
}

func (c *terminalClient) KillTerminal(context.Context, *acp1.KillTerminalRequest) (*acp1.KillTerminalResponse, error) {
	c.record("kill")
	return &acp1.KillTerminalResponse{}, nil
}

func (c *terminalClient) ReleaseTerminal(context.Context, *acp1.ReleaseTerminalRequest) (*acp1.ReleaseTerminalResponse, error) {
	c.record("release")
	return &acp1.ReleaseTerminalResponse{}, nil
}

func (c *terminalClient) callLog() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.calls)
}

func TestRunTerminalExits(t *testing.T) {
	client := &terminalClient{exitCode: 2}
	stream := acp1.NewSessionStream(client, "s1")
	run, err := stream.RunTerminal(t.Context(), "call_1", acp1.CreateTerminalRequest{Command: "make"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if run.TerminalID != "term_1" || run.Output != "ok\n" || run.TimedOut || run.ExitStatus.GetExitCode() != 2 {
		t.Fatalf("run = %+v", run)
	}
	if got, want := client.callLog(), []string{"create make", "wait", "output", "release"}; !slices.Equal(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	// The tool call shows the terminal while the command runs.
	updates := client.Updates()
	if len(updates) != 1 {
		t.Fatalf("updates = %d, want the tool call's", len(updates))
	}
	update, _ := updates[0].Update.As[acp1.SessionUpdateToolCallUpdate]()
	if update.ToolCallID != "call_1" || update.GetStatus() != acp1.ToolCallStatusInProgress || len(update.Content) != 1 {
		t.Fatalf("tool call update = %+v", update)
	}
}

func TestRunTerminalTimesOut(t *testing.T) {
	client := &terminalClient{hang: true}
	stream := acp1.NewSessionStream(client, "s1")
	run, err := stream.RunTerminal(t.Context(), "", acp1.CreateTerminalRequest{Command: "sleep"}, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !run.TimedOut || run.ExitStatus != nil || run.Output != "ok\n" {
		t.Fatalf("run = %+v", run)
	}
	if got, want := client.callLog(), []string{"create sleep", "wait", "kill", "output", "release"}; !slices.Equal(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if len(client.Updates()) != 0 {
		t.Fatal("a run without a tool call sent an update")
	}
}

func TestRunTerminalCancelled(t *testing.T) {
	client := &terminalClient{hang: true}
	stream := acp1.NewSessionStream(client, "s1")
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(10*time.Millisecond, cancel)
	run, err := stream.RunTerminal(ctx, "", acp1.CreateTerminalRequest{Command: "sleep"}, 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if run == nil || run.TimedOut || run.Output != "ok\n" {
		t.Fatalf("run = %+v", run)
	}
	if got, want := client.callLog(), []string{"create sleep", "wait", "kill", "output", "release"}; !slices.Equal(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
}
