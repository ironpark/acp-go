package main

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"time"

	acp "github.com/ironpark/acp-go"
)

// ExampleAgent implements the acp.Agent interface with full session update capabilities
type ExampleAgent struct {
	client   acp.Client
	sessions map[acp.SessionId]*AgentSession
}

// AgentSession holds session state
type AgentSession struct {
	sessionId     acp.SessionId
	cancelContext context.Context
	cancelFunc    context.CancelFunc
}

func NewExampleAgent() *ExampleAgent {
	return &ExampleAgent{
		sessions: make(map[acp.SessionId]*AgentSession),
	}
}

func (a *ExampleAgent) Initialize(ctx context.Context, params *acp.InitializeRequest) (*acp.InitializeResponse, error) {
	return &acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersion(acp.CurrentProtocolVersion),
		AgentCapabilities: &acp.AgentCapabilities{
			LoadSession: false,
			McpCapabilities: &acp.McpCapabilities{
				Http: false,
				Sse:  false,
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

func (a *ExampleAgent) Authenticate(ctx context.Context, params *acp.AuthenticateRequest) error {
	return nil
}

func (a *ExampleAgent) NewSession(ctx context.Context, params *acp.NewSessionRequest) (*acp.NewSessionResponse, error) {
	// Generate a random session ID
	sessionId := acp.SessionId(fmt.Sprintf("session_%s", generateRandomID()))

	// Create cancellation context for this session
	sessionCtx, cancelFunc := context.WithCancel(context.Background())

	session := &AgentSession{
		sessionId:     sessionId,
		cancelContext: sessionCtx,
		cancelFunc:    cancelFunc,
	}

	a.sessions[sessionId] = session

	return &acp.NewSessionResponse{
		SessionId: sessionId,
		Modes:     nil,
	}, nil
}

func (a *ExampleAgent) LoadSession(ctx context.Context, params *acp.LoadSessionRequest) (*acp.LoadSessionResponse, error) {
	return nil, fmt.Errorf("load session not supported")
}

func (a *ExampleAgent) SetSessionMode(ctx context.Context, params *acp.SetSessionModeRequest) error {
	return nil
}

func (a *ExampleAgent) Prompt(ctx context.Context, params *acp.PromptRequest) (*acp.PromptResponse, error) {
	session, exists := a.sessions[params.SessionId]
	if !exists {
		return nil, fmt.Errorf("session %s not found", params.SessionId)
	}

	// Cancel any previous prompt processing for this session
	session.cancelFunc()
	sessionCtx, cancelFunc := context.WithCancel(context.Background())
	session.cancelContext = sessionCtx
	session.cancelFunc = cancelFunc

	// Simulate the turn processing
	err := a.simulateTurn(sessionCtx, params.SessionId)
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
	if session, exists := a.sessions[params.SessionId]; exists {
		session.cancelFunc()
	}
	return nil
}

func (a *ExampleAgent) simulateTurn(ctx context.Context, sessionId acp.SessionId) error {
	// Send initial agent message chunk
	err := a.client.SessionUpdate(ctx, &acp.SessionNotification{
		SessionId: sessionId,
		Update:    acp.NewSessionUpdateAgentMessageChunk(acp.NewContentBlockText("I'll help you with that. Let me start by reading some files to understand the current situation.")),
	})
	if err != nil {
		return err
	}

	// Simulate model thinking time
	if err := a.simulateModelInteraction(ctx); err != nil {
		return err
	}

	// Send a tool call that doesn't need permission
	toolCallId := acp.ToolCallId("call_1")
	err = a.client.SessionUpdate(ctx, &acp.SessionNotification{
		SessionId: sessionId,
		Update: acp.NewSessionUpdateToolCall(
			toolCallId,
			"Reading project files",
			acp.ToolKindPtr(acp.ToolKindRead),
			acp.ToolCallStatusPtr(acp.ToolCallStatusPending),
			[]acp.ToolCallLocation{{Path: "/project/README.md"}},
			map[string]any{"path": "/project/README.md"},
		),
	})
	if err != nil {
		return err
	}

	if err := a.simulateModelInteraction(ctx); err != nil {
		return err
	}

	// Update tool call to completed
	err = a.client.SessionUpdate(ctx, &acp.SessionNotification{
		SessionId: sessionId,
		Update: acp.NewSessionUpdateToolCallUpdate(
			toolCallId,
			acp.ToolCallStatusPtr(acp.ToolCallStatusCompleted),
			[]acp.ToolCallContent{
				acp.NewToolCallContentContent(acp.NewContentBlockText("# My Project\n\nThis is a sample project...")),
			},
			map[string]any{"content": "# My Project\n\nThis is a sample project..."},
		),
	})
	if err != nil {
		return err
	}

	if err := a.simulateModelInteraction(ctx); err != nil {
		return err
	}

	// Send more agent message
	err = a.client.SessionUpdate(ctx, &acp.SessionNotification{
		SessionId: sessionId,
		Update:    acp.NewSessionUpdateAgentMessageChunk(acp.NewContentBlockText(" Now I understand the project structure. I need to make some changes to improve it.")),
	})
	if err != nil {
		return err
	}

	if err := a.simulateModelInteraction(ctx); err != nil {
		return err
	}

	// Send a tool call that DOES need permission
	toolCallId2 := acp.ToolCallId("call_2")
	err = a.client.SessionUpdate(ctx, &acp.SessionNotification{
		SessionId: sessionId,
		Update: acp.NewSessionUpdateToolCall(
			toolCallId2,
			"Modifying critical configuration file",
			acp.ToolKindPtr(acp.ToolKindEdit),
			acp.ToolCallStatusPtr(acp.ToolCallStatusPending),
			[]acp.ToolCallLocation{{Path: "/project/config.json"}},
			map[string]any{
				"path":    "/project/config.json",
				"content": `{"database": {"host": "new-host"}}`,
			},
		),
	})
	if err != nil {
		return err
	}

	// Request permission for the sensitive operation
	permissionResponse, err := a.client.RequestPermission(ctx, &acp.RequestPermissionRequest{
		SessionId: sessionId,
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: toolCallId2,
			Title:      "Modifying critical configuration file",
			Kind:       acp.ToolKindPtr(acp.ToolKindEdit),
			Status:     acp.ToolCallStatusPtr(acp.ToolCallStatusPending),
			Locations: []acp.ToolCallLocation{
				{Path: "/home/user/project/config.json"},
			},
			RawInput: nil,
		},
		Options: []acp.PermissionOption{
			{
				Kind:     acp.PermissionOptionKindAllowOnce,
				Name:     "Allow this change",
				OptionId: acp.PermissionOptionId("allow"),
			},
			{
				Kind:     acp.PermissionOptionKindRejectOnce,
				Name:     "Skip this change",
				OptionId: acp.PermissionOptionId("reject"),
			},
		},
	})
	if err != nil {
		return err
	}

	// Handle permission response
	if permissionResponse.Outcome.IsCancelled() {
		return nil
	}

	if selectedOutcome := permissionResponse.Outcome.GetSelected(); selectedOutcome != nil {
		switch selectedOutcome.OptionId {
		case "allow":
			err = a.client.SessionUpdate(ctx, &acp.SessionNotification{
				SessionId: sessionId,
				Update: acp.NewSessionUpdateToolCallUpdate(
					toolCallId2,
					acp.ToolCallStatusPtr(acp.ToolCallStatusCompleted),
					nil,
					map[string]any{
						"success": true,
						"message": "Configuration updated",
					},
				),
			})
			if err != nil {
				return err
			}

			if err := a.simulateModelInteraction(ctx); err != nil {
				return err
			}

			err = a.client.SessionUpdate(ctx, &acp.SessionNotification{
				SessionId: sessionId,
				Update:    acp.NewSessionUpdateAgentMessageChunk(acp.NewContentBlockText(" Perfect! I've successfully updated the configuration. The changes have been applied.")),
			})
			if err != nil {
				return err
			}

		case "reject":
			if err := a.simulateModelInteraction(ctx); err != nil {
				return err
			}

			err = a.client.SessionUpdate(ctx, &acp.SessionNotification{
				SessionId: sessionId,
				Update:    acp.NewSessionUpdateAgentMessageChunk(acp.NewContentBlockText(" I understand you prefer not to make that change. I'll skip the configuration update.")),
			})
			if err != nil {
				return err
			}

		default:
			return fmt.Errorf("unexpected permission outcome: %s", selectedOutcome.OptionId)
		}
	} else {
		return fmt.Errorf("unexpected permission outcome type")
	}

	return nil
}

func (a *ExampleAgent) simulateModelInteraction(ctx context.Context) error {
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

	// Create connection using stdin/stdout
	conn := acp.NewAgentSideConnection(agent, os.Stdin, os.Stdout)

	// Set the client reference so the agent can make requests
	agent.client = conn.Client()

	// Start the connection
	if err := conn.Start(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "Connection error: %v\n", err)
		os.Exit(1)
	}
}
