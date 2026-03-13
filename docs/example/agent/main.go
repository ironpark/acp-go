package main

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"time"

	acp "github.com/ironpark/go-acp"
)

// ExampleAgent implements the acp.Agent interface with full session update capabilities.
//
// This example demonstrates:
//   - SessionStream for convenient update sending
//   - Middleware for logging and panic recovery
//   - Tool call lifecycle (start → complete/fail)
//   - Permission requests
type ExampleAgent struct {
	client   acp.Client
	sessions map[acp.SessionID]*AgentSession
}

// AgentSession holds session state
type AgentSession struct {
	sessionId     acp.SessionID
	cancelContext context.Context
	cancelFunc    context.CancelFunc
}

func NewExampleAgent() *ExampleAgent {
	return &ExampleAgent{
		sessions: make(map[acp.SessionID]*AgentSession),
	}
}

func (a *ExampleAgent) Initialize(ctx context.Context, params *acp.InitializeRequest) (*acp.InitializeResponse, error) {
	return &acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersion(acp.CurrentProtocolVersion),
		AgentCapabilities: &acp.AgentCapabilities{
			LoadSession: false,
			MCPCapabilities: &acp.MCPCapabilities{
				HTTP: false,
				SSE:  false,
			},
			PromptCapabilities: &acp.PromptCapabilities{
				Audio:           false,
				EmbeddedContext: false,
				Image:           false,
			},
		},
		AuthMethods: []acp.AuthMethod{},
	}, nil
}

func (a *ExampleAgent) Authenticate(ctx context.Context, params *acp.AuthenticateRequest) (*acp.AuthenticateResponse, error) {
	return &acp.AuthenticateResponse{}, nil
}

func (a *ExampleAgent) NewSession(ctx context.Context, params *acp.NewSessionRequest) (*acp.NewSessionResponse, error) {
	sessionId := acp.SessionID(fmt.Sprintf("session_%s", generateRandomID()))

	sessionCtx, cancelFunc := context.WithCancel(context.Background())
	session := &AgentSession{
		sessionId:     sessionId,
		cancelContext: sessionCtx,
		cancelFunc:    cancelFunc,
	}
	a.sessions[sessionId] = session

	return &acp.NewSessionResponse{
		SessionID: sessionId,
		Modes:     nil,
	}, nil
}

func (a *ExampleAgent) LoadSession(ctx context.Context, params *acp.LoadSessionRequest) (*acp.LoadSessionResponse, error) {
	return nil, acp.ErrMethodNotFound("session/load")
}

func (a *ExampleAgent) ListSessions(ctx context.Context, params *acp.ListSessionsRequest) (*acp.ListSessionsResponse, error) {
	return &acp.ListSessionsResponse{}, nil
}

func (a *ExampleAgent) SetSessionMode(ctx context.Context, params *acp.SetSessionModeRequest) (*acp.SetSessionModeResponse, error) {
	return &acp.SetSessionModeResponse{}, nil
}

func (a *ExampleAgent) SetSessionConfigOption(ctx context.Context, params *acp.SetSessionConfigOptionRequest) (*acp.SetSessionConfigOptionResponse, error) {
	return &acp.SetSessionConfigOptionResponse{ConfigOptions: []acp.SessionConfigOption{}}, nil
}

func (a *ExampleAgent) Prompt(ctx context.Context, params *acp.PromptRequest) (*acp.PromptResponse, error) {
	session, exists := a.sessions[params.SessionID]
	if !exists {
		return nil, fmt.Errorf("session %s not found", params.SessionID)
	}

	// Cancel any previous prompt processing for this session
	session.cancelFunc()
	sessionCtx, cancelFunc := context.WithCancel(context.Background())
	session.cancelContext = sessionCtx
	session.cancelFunc = cancelFunc

	err := a.simulateTurn(sessionCtx, params.SessionID)
	if err != nil {
		if sessionCtx.Err() == context.Canceled {
			return &acp.PromptResponse{
				StopReason: acp.StopReasonCancelled,
			}, nil
		}
		return nil, err
	}

	return &acp.PromptResponse{
		StopReason: acp.StopReasonEndTurn,
	}, nil
}

func (a *ExampleAgent) Cancel(ctx context.Context, params *acp.CancelNotification) error {
	if session, exists := a.sessions[params.SessionID]; exists {
		session.cancelFunc()
	}
	return nil
}

func (a *ExampleAgent) simulateTurn(ctx context.Context, sessionId acp.SessionID) error {
	// Use SessionStream for convenient update sending
	stream := acp.NewSessionStream(a.client, sessionId)

	// Send initial agent message
	if err := stream.SendText(ctx, "I'll help you with that. Let me start by reading some files."); err != nil {
		return err
	}

	if err := simulateDelay(ctx); err != nil {
		return err
	}

	// Tool call: read file (no permission needed)
	toolCallId := acp.ToolCallID("call_1")
	if err := stream.StartToolCall(ctx, toolCallId, "Reading project files", acp.ToolKindRead); err != nil {
		return err
	}

	if err := simulateDelay(ctx); err != nil {
		return err
	}

	// Complete the tool call with content
	if err := stream.CompleteToolCall(ctx, toolCallId,
		acp.NewToolCallContentContent(acp.NewContentBlockText("# My Project\n\nThis is a sample project...")),
	); err != nil {
		return err
	}

	if err := simulateDelay(ctx); err != nil {
		return err
	}

	if err := stream.SendText(ctx, " Now I understand the project. I need to make changes."); err != nil {
		return err
	}

	if err := simulateDelay(ctx); err != nil {
		return err
	}

	// Tool call: edit file (needs permission)
	toolCallId2 := acp.ToolCallID("call_2")
	if err := stream.StartToolCall(ctx, toolCallId2, "Modifying configuration", acp.ToolKindEdit); err != nil {
		return err
	}

	// Request permission for the sensitive operation
	permissionResponse, err := a.client.RequestPermission(ctx, &acp.RequestPermissionRequest{
		SessionID: sessionId,
		ToolCall: acp.ToolCallUpdate{
			ToolCallID: toolCallId2,
			Title:      "Modifying configuration",
			Kind:       new(acp.ToolKindEdit),
			Status:     new(acp.ToolCallStatusPending),
			Locations:  []acp.ToolCallLocation{{Path: "/project/config.json"}},
		},
		Options: []acp.PermissionOption{
			{Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow this change", OptionID: "allow"},
			{Kind: acp.PermissionOptionKindRejectOnce, Name: "Skip this change", OptionID: "reject"},
		},
	})
	if err != nil {
		return err
	}

	// Handle permission response using Match pattern
	return acp.MatchRequestPermissionOutcome[error](&permissionResponse.Outcome, acp.RequestPermissionOutcomeMatcher[error]{
		Cancelled: func(_ acp.RequestPermissionOutcomeCancelled) error {
			return stream.FailToolCall(ctx, toolCallId2)
		},
		Selected: func(v acp.RequestPermissionOutcomeSelected) error {
			if v.OptionID == "allow" {
				if err := stream.CompleteToolCall(ctx, toolCallId2); err != nil {
					return err
				}
				if err := simulateDelay(ctx); err != nil {
					return err
				}
				return stream.SendText(ctx, " Configuration updated successfully.")
			}
			if err := stream.FailToolCall(ctx, toolCallId2); err != nil {
				return err
			}
			return stream.SendText(ctx, " Skipping the configuration update.")
		},
	})
}

func simulateDelay(ctx context.Context) error {
	select {
	case <-time.After(1 * time.Second):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func generateRandomID() string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 8)
	for i := range b {
		b[i] = charset[rand.Intn(len(charset))]
	}
	return string(b)
}

func main() {
	agent := NewExampleAgent()

	// Create connection with middleware
	conn := acp.NewAgentSideConnection(agent, os.Stdin, os.Stdout,
		acp.WithMiddleware(acp.RecoveryMiddleware()),
	)

	agent.client = conn.Client()

	if err := conn.Start(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "Connection error: %v\n", err)
		os.Exit(1)
	}
}
