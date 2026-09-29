package acp1_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	acp "github.com/ironpark/acp-go"
	"github.com/ironpark/acp-go/acp1"
	"github.com/ironpark/acp-go/acp1/acp1test"
)

// commandSession offers one slash command.
type commandSession struct{}

func (commandSession) AvailableCommands() []acp1.AvailableCommand {
	return []acp1.AvailableCommand{{Name: "compact", Description: "Summarize the conversation"}}
}

// helperAgent serves sessions from an embedded manager and answers prompts
// with prompt.
type helperAgent struct {
	*acp1.SessionManager[commandSession]
	prompt func(ctx context.Context, params *acp1.PromptRequest) (*acp1.PromptResponse, error)
}

func (*helperAgent) Initialize(context.Context, *acp1.InitializeRequest) (*acp1.InitializeResponse, error) {
	return &acp1.InitializeResponse{ProtocolVersion: acp1.ProtocolVersion}, nil
}

func (a *helperAgent) Prompt(ctx context.Context, params *acp1.PromptRequest) (*acp1.PromptResponse, error) {
	return a.prompt(ctx, params)
}

func newHelperAgent(store acp1.SessionStore[commandSession], opts ...acp1.SessionManagerOption) *helperAgent {
	return &helperAgent{SessionManager: acp1.NewSessionManager(store,
		func(context.Context, *acp1.NewSessionRequest) (acp1.SessionID, commandSession, error) {
			return acp1.GenerateSessionID(), commandSession{}, nil
		}, opts...)}
}

func isCommands(id acp1.SessionID) func(*acp1.SessionNotification) bool {
	return func(n *acp1.SessionNotification) bool {
		_, ok := n.Update.As[acp1.SessionUpdateAvailableCommandsUpdate]()
		return ok && n.SessionID == id
	}
}

func TestSessionManagerAdvertisesCommands(t *testing.T) {
	client := &acp1test.Client{}
	agent := newHelperAgent(acp1.NewMemoryStore[commandSession]())
	conn := acp1test.Connect(t, func(*acp1.AgentSideConnection) acp1.Agent { return agent }, client)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	created, err := conn.NewSession(ctx, &acp1.NewSessionRequest{Cwd: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	update, err := client.WaitFor(ctx, isCommands(created.SessionID))
	if err != nil {
		t.Fatalf("no commands after session/new: %v", err)
	}
	commands, _ := update.Update.As[acp1.SessionUpdateAvailableCommandsUpdate]()
	if len(commands.AvailableCommands) != 1 || commands.AvailableCommands[0].Name != "compact" {
		t.Errorf("commands = %+v", commands.AvailableCommands)
	}

	if _, err := conn.ResumeSession(ctx, &acp1.ResumeSessionRequest{SessionID: created.SessionID, Cwd: "/tmp"}); err != nil {
		t.Fatal(err)
	}
	seen := 0 // WaitFor shows each update to match once
	if _, err := client.WaitFor(ctx, func(n *acp1.SessionNotification) bool {
		if isCommands(created.SessionID)(n) {
			seen++
		}
		return seen == 2
	}); err != nil {
		t.Fatalf("no commands after session/resume: %v", err)
	}
}

// fsClient reads files, as an editor does.
type fsClient struct{ acp1test.Client }

func (*fsClient) ReadTextFile(_ context.Context, params *acp1.ReadTextFileRequest) (*acp1.ReadTextFileResponse, error) {
	return &acp1.ReadTextFileResponse{Content: "contents of " + params.Path}, nil
}

func TestClientCapabilitiesGateClientCalls(t *testing.T) {
	var agentConn *acp1.AgentSideConnection
	agent := newHelperAgent(acp1.NewMemoryStore[commandSession]())
	conn := acp1test.Connect(t, func(c *acp1.AgentSideConnection) acp1.Agent {
		agentConn = c
		return agent
	}, &fsClient{})
	ctx := t.Context()
	if caps := agentConn.ClientCapabilities(); caps != nil {
		t.Fatalf("capabilities before initialize = %+v", caps)
	}
	stream := acp1.NewSessionStream(agentConn, "s1")

	// Before initialize the capabilities are unknown and calls go through.
	if got, err := stream.ReadTextFile(ctx, "/a"); err != nil || got != "contents of /a" {
		t.Fatalf("ReadTextFile before initialize = %q, %v", got, err)
	}

	if _, err := conn.Initialize(ctx, &acp1.InitializeRequest{
		ProtocolVersion:    acp1.ProtocolVersion,
		ClientCapabilities: &acp1.ClientCapabilities{FS: &acp1.FileSystemCapabilities{ReadTextFile: new(true)}},
	}); err != nil {
		t.Fatal(err)
	}
	caps := agentConn.ClientCapabilities()
	if !caps.GetFS().GetReadTextFile() || caps.GetFS().GetWriteTextFile() || caps.GetTerminal() {
		t.Fatalf("capabilities = %+v", caps)
	}
	if got, err := stream.ReadTextFile(ctx, "/a"); err != nil || got != "contents of /a" {
		t.Fatalf("ReadTextFile = %q, %v", got, err)
	}
	if err := stream.WriteTextFile(ctx, "/a", "x"); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("WriteTextFile = %v, want errors.ErrUnsupported", err)
	}
	if _, err := stream.NewTerminal(ctx, acp1.CreateTerminalRequest{Command: "ls"}); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("NewTerminal = %v, want errors.ErrUnsupported", err)
	}
}

func TestRunTurnResponse(t *testing.T) {
	manager := newHelperAgent(acp1.NewMemoryStore[commandSession]()).SessionManager
	created, err := manager.NewSession(t.Context(), &acp1.NewSessionRequest{Cwd: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	usage := &acp1.Usage{InputTokens: 3, OutputTokens: 4, TotalTokens: 7}
	response, err := manager.RunTurnResponse(t.Context(), created.SessionID, func(context.Context, commandSession) (*acp1.PromptResponse, error) {
		return &acp1.PromptResponse{StopReason: acp1.StopReasonMaxTokens, Usage: usage}, nil
	})
	if err != nil || response.StopReason != acp1.StopReasonMaxTokens || response.Usage != usage {
		t.Fatalf("RunTurnResponse = %+v, %v", response, err)
	}
	if _, err := manager.RunTurnResponse(t.Context(), created.SessionID, func(context.Context, commandSession) (*acp1.PromptResponse, error) {
		return nil, nil
	}); !acp.IsCode(err, acp.ErrorCodeInternalError) {
		t.Fatalf("RunTurnResponse without a response = %v, want an internal error", err)
	}
}

func TestRunTurnResponseKeepsTheCancelledResponse(t *testing.T) {
	started := make(chan struct{})
	agent := newHelperAgent(acp1.NewMemoryStore[commandSession]())
	usage := &acp1.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}
	agent.prompt = func(ctx context.Context, params *acp1.PromptRequest) (*acp1.PromptResponse, error) {
		return agent.RunTurnResponse(ctx, params.SessionID, func(ctx context.Context, _ commandSession) (*acp1.PromptResponse, error) {
			close(started)
			<-ctx.Done()
			if !acp.TurnCancelled(ctx) {
				t.Errorf("turn ended by %v, not the client's cancel", context.Cause(ctx))
			}
			return &acp1.PromptResponse{StopReason: acp1.StopReasonEndTurn, Usage: usage}, nil
		})
	}
	conn := acp1test.Connect(t, func(*acp1.AgentSideConnection) acp1.Agent { return agent }, &acp1test.Client{})
	session, err := conn.StartSession(t.Context(), &acp1.NewSessionRequest{Cwd: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := session.Prompt(t.Context(), acp1.TextBlock("work"))
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if err := session.Cancel(t.Context()); err != nil {
		t.Fatal(err)
	}
	response, err := turn.Wait()
	if err != nil || response.StopReason != acp1.StopReasonCancelled || response.Usage == nil || response.Usage.TotalTokens != 2 {
		t.Fatalf("response = %+v, %v; want cancelled with the usage", response, err)
	}
}

// countingStore counts the sets of a memory store, and fails them with err
// when it is set.
type countingStore struct {
	*acp1.MemoryStore[commandSession]
	sets atomic.Int32
	err  error
}

func (s *countingStore) Set(ctx context.Context, id acp1.SessionID, session commandSession) error {
	s.sets.Add(1)
	if s.err != nil {
		return s.err
	}
	return s.MemoryStore.Set(ctx, id, session)
}

func TestWithAutoSave(t *testing.T) {
	store := &countingStore{MemoryStore: acp1.NewMemoryStore[commandSession]()}
	var mu sync.Mutex
	var failures []error
	manager := newHelperAgent(store, acp1.WithAutoSave(func(_ acp1.SessionID, err error) {
		mu.Lock()
		failures = append(failures, err)
		mu.Unlock()
	})).SessionManager
	created, err := manager.NewSession(t.Context(), &acp1.NewSessionRequest{Cwd: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	run := func(context.Context, commandSession) (acp1.StopReason, error) { return acp1.StopReasonEndTurn, nil }

	if _, err := manager.RunTurn(t.Context(), created.SessionID, run); err != nil {
		t.Fatal(err)
	}
	if got := store.sets.Load(); got != 2 {
		t.Fatalf("sets after one turn = %d, want the creation and the turn's", got)
	}

	// A turn that fails is saved too, since it may have changed the session.
	if _, err := manager.RunTurn(t.Context(), created.SessionID, func(context.Context, commandSession) (acp1.StopReason, error) {
		return "", errors.New("model unavailable")
	}); err == nil {
		t.Fatal("the failed turn succeeded")
	}
	if got := store.sets.Load(); got != 3 {
		t.Fatalf("sets after a failed turn = %d, want 3", got)
	}

	// A session deleted during its turn is not saved back.
	if _, err := manager.RunTurn(t.Context(), created.SessionID, func(ctx context.Context, _ commandSession) (acp1.StopReason, error) {
		_, err := manager.DeleteSession(ctx, &acp1.DeleteSessionRequest{SessionID: created.SessionID})
		return acp1.StopReasonEndTurn, err
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.Get(t.Context(), created.SessionID); ok || store.sets.Load() != 3 {
		t.Fatalf("deleted session saved back: present %v, sets %d", ok, store.sets.Load())
	}

	// A failed save goes to the error callback.
	again, err := manager.NewSession(t.Context(), &acp1.NewSessionRequest{Cwd: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	store.err = errors.New("disk full")
	if _, err := manager.RunTurn(t.Context(), again.SessionID, run); err != nil {
		t.Fatalf("a failed save failed the turn: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(failures) != 1 || !errors.Is(failures[0], store.err) {
		t.Fatalf("save failures = %v", failures)
	}
}

func TestProposeToolCall(t *testing.T) {
	client := &acp1test.Client{}
	stream := acp1.NewSessionStream(client, "s1")
	if err := stream.ProposeToolCall(t.Context(), "call_1", "Run tests", acp1.ToolKindExecute); err != nil {
		t.Fatal(err)
	}
	call, ok := client.Updates()[0].Update.As[acp1.SessionUpdateToolCall]()
	if !ok || call.GetStatus() != acp1.ToolCallStatusPending || call.Title != "Run tests" {
		t.Fatalf("update = %+v", client.Updates()[0].Update)
	}
}

func TestConfigHelpers(t *testing.T) {
	option := acp1.NewSessionConfigOption(acp1.SessionConfigOptionSelect{
		ID: "model", Name: "Model", CurrentValue: "fast",
		Options: acp1.SelectOptions(acp1.SessionConfigSelectOption{Value: "fast", Name: "Fast"}),
	})
	selected, _ := option.As[acp1.SessionConfigOptionSelect]()
	choices, err := selected.Options.As[[]acp1.SessionConfigSelectOption]()
	if err != nil || len(choices) != 1 || choices[0].Value != "fast" {
		t.Fatalf("choices = %+v, %v", choices, err)
	}
	if empty, err := acp1.SelectOptions().As[[]acp1.SessionConfigSelectOption](); err != nil || len(empty) != 0 {
		t.Fatalf("empty choices = %+v, %v", empty, err)
	}

	change, ok := acp1.ConfigChangeOf(new(acp1.NewSetSessionConfigOptionRequest(acp1.SetSessionConfigOptionRequestUntagged{
		SessionID: "s1", ConfigID: "model", Value: "smart",
	})))
	if !ok || change.SessionID != "s1" || change.ConfigID != "model" || change.Value != "smart" || change.Boolean != nil {
		t.Fatalf("select change = %+v, %v", change, ok)
	}
	change, ok = acp1.ConfigChangeOf(new(acp1.NewSetSessionConfigOptionRequest(acp1.SetSessionConfigOptionRequestBoolean{
		SessionID: "s1", ConfigID: "fast_mode", Value: true,
	})))
	if !ok || change.ConfigID != "fast_mode" || change.Boolean == nil || !*change.Boolean {
		t.Fatalf("boolean change = %+v, %v", change, ok)
	}
	if _, ok := acp1.ConfigChangeOf(&acp1.SetSessionConfigOptionRequest{}); ok {
		t.Fatal("a zero request reads as a change")
	}
}
