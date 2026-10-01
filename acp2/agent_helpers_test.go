package acp2_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ironpark/acp-go/acp2"
	"github.com/ironpark/acp-go/acp2/acp2test"
	schema "github.com/ironpark/acp-go/schema/v2"
)

// commandSession offers one slash command.
type commandSession struct{}

func (commandSession) SessionInfo() acp2.SessionInfo { return acp2.SessionInfo{Cwd: "/tmp"} }

func (commandSession) AvailableCommands() []acp2.AvailableCommand {
	return []acp2.AvailableCommand{{Name: "compact", Description: "Summarize the conversation"}}
}

// helperAgent serves sessions from an embedded manager and ends each turn
// at once.
type helperAgent struct {
	*acp2.SessionManager[commandSession]
	client acp2.Client
}

func (*helperAgent) Initialize(context.Context, *acp2.InitializeRequest) (*acp2.InitializeResponse, error) {
	return &acp2.InitializeResponse{ProtocolVersion: acp2.ProtocolVersion, Info: schema.Implementation{Name: "helper", Version: "0"}}, nil
}

func (a *helperAgent) Prompt(ctx context.Context, params *acp2.PromptRequest) (*acp2.PromptResponse, error) {
	stream := acp2.NewSessionStream(a.client, params.SessionID)
	if _, err := a.StartTurn(ctx, params.SessionID, stream, func(context.Context, commandSession) acp2.StopReason {
		return acp2.StopReasonEndTurn
	}); err != nil {
		return nil, err
	}
	return &acp2.PromptResponse{MessageID: acp2.GenerateMessageID()}, nil
}

func connectHelper(t *testing.T, store acp2.SessionStore[commandSession], client acp2.Client, opts ...acp2.SessionManagerOption) (*acp2.ClientSideConnection, *acp2.AgentSideConnection) {
	agent := &helperAgent{SessionManager: acp2.NewSessionManager(store,
		func(context.Context, *acp2.NewSessionRequest) (acp2.SessionID, commandSession, error) {
			return acp2.GenerateSessionID(), commandSession{}, nil
		}, opts...)}
	var agentConn *acp2.AgentSideConnection
	conn := acp2test.Connect(t, func(c *acp2.AgentSideConnection) acp2.Agent {
		agentConn, agent.client = c, c
		return agent
	}, client)
	return conn, agentConn
}

func TestSessionManagerAdvertisesCommands(t *testing.T) {
	conn, _ := connectHelper(t, acp2.NewMemoryStore[commandSession](), &acp2test.Client{})
	ctx := t.Context()

	created, err := conn.NewSession(ctx, &acp2.NewSessionRequest{Cwd: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	if len(created.AvailableCommands) != 1 || created.AvailableCommands[0].Name != "compact" {
		t.Errorf("session/new commands = %+v", created.AvailableCommands)
	}
	resumed, err := conn.ResumeSession(ctx, &acp2.ResumeSessionRequest{SessionID: created.SessionID, Cwd: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed.AvailableCommands) != 1 || resumed.AvailableCommands[0].Name != "compact" {
		t.Errorf("session/resume commands = %+v", resumed.AvailableCommands)
	}
}

func TestClientCapabilitiesRecorded(t *testing.T) {
	conn, agentConn := connectHelper(t, acp2.NewMemoryStore[commandSession](), &acp2test.Client{})
	if caps := agentConn.ClientCapabilities(); caps != nil {
		t.Fatalf("capabilities before initialize = %+v", caps)
	}
	if _, err := conn.Initialize(t.Context(), &acp2.InitializeRequest{
		ProtocolVersion: acp2.ProtocolVersion,
		Info:            schema.Implementation{Name: "test-client", Version: "0"},
	}); err != nil {
		t.Fatal(err)
	}
	if caps := agentConn.ClientCapabilities(); caps == nil {
		t.Fatal("no capabilities after initialize")
	}
}

// countingStore counts the sets of a memory store.
type countingStore struct {
	*acp2.MemoryStore[commandSession]
	sets atomic.Int32
}

func (s *countingStore) Set(ctx context.Context, id acp2.SessionID, session commandSession) error {
	s.sets.Add(1)
	return s.MemoryStore.Set(ctx, id, session)
}

func TestWithAutoSave(t *testing.T) {
	store := &countingStore{MemoryStore: acp2.NewMemoryStore[commandSession]()}
	conn, _ := connectHelper(t, store, &acp2test.Client{}, acp2.WithAutoSave(nil))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	session, err := conn.StartSession(ctx, &acp2.NewSessionRequest{Cwd: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	turn, _, err := session.Prompt(ctx, acp2.TextBlock("hi"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := turn.Wait(); err != nil {
		t.Fatal(err)
	}
	// The idle report that ends the client's turn goes out before the
	// agent's turn ends and saves, so wait for the save itself.
	for store.sets.Load() < 2 {
		select {
		case <-ctx.Done():
			t.Fatalf("sets = %d, want the creation and the turn's", store.sets.Load())
		case <-time.After(time.Millisecond):
		}
	}
}

func TestProposeToolCall(t *testing.T) {
	client := &acp2test.Client{}
	stream := acp2.NewSessionStream(client, "s1")
	if err := stream.ProposeToolCall(t.Context(), "call_1", "Run tests", acp2.ToolKindExecute); err != nil {
		t.Fatal(err)
	}
	call, ok := client.Updates()[0].Update.As[acp2.SessionUpdateToolCallUpdate]()
	if !ok || call.GetStatus() != acp2.ToolCallStatusPending || call.GetTitle() != "Run tests" {
		t.Fatalf("update = %+v", client.Updates()[0].Update)
	}
}

func TestConfigHelpers(t *testing.T) {
	choices, err := acp2.SelectOptions(acp2.SessionConfigSelectOption{Value: "fast", Name: "Fast"}).As[[]acp2.SessionConfigSelectOption]()
	if err != nil || len(choices) != 1 || choices[0].Value != "fast" {
		t.Fatalf("choices = %+v, %v", choices, err)
	}
	change, ok := acp2.ConfigChangeOf(new(acp2.NewSetSessionConfigOptionRequest(acp2.SetSessionConfigOptionRequestID{
		SessionID: "s1", ConfigID: "model", Value: "smart",
	})))
	if !ok || change.ConfigID != "model" || change.Value != "smart" || change.Boolean != nil || change.Custom != nil {
		t.Fatalf("select change = %+v, %v", change, ok)
	}
	change, ok = acp2.ConfigChangeOf(new(acp2.NewSetSessionConfigOptionRequest(acp2.SetSessionConfigOptionRequestBoolean{
		SessionID: "s1", ConfigID: "fast_mode", Value: true,
	})))
	if !ok || change.Boolean == nil || !*change.Boolean {
		t.Fatalf("boolean change = %+v, %v", change, ok)
	}
	if _, ok := acp2.ConfigChangeOf(&acp2.SetSessionConfigOptionRequest{}); ok {
		t.Fatal("a zero request reads as a change")
	}
}
