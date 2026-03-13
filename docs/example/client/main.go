package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	acp "github.com/ironpark/go-acp"
)

// ExampleClient implements the acp.Client interface.
//
// This example demonstrates:
//   - MatchSessionUpdate for exhaustive update handling
//   - MatchContentBlock for content type dispatch
//   - SpawnAgent for easy agent subprocess management
type ExampleClient struct{}

func (c *ExampleClient) RequestPermission(ctx context.Context, params *acp.RequestPermissionRequest) (*acp.RequestPermissionResponse, error) {
	fmt.Printf("\n🔐 Permission requested: %s\n", params.ToolCall.Title)

	fmt.Println("\nOptions:")
	for i, option := range params.Options {
		fmt.Printf("   %d. %s (%s)\n", i+1, option.Name, option.Kind)
	}

	reader := bufio.NewReader(os.Stdin)

	for {
		fmt.Print("\nChoose an option: ")
		answer, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}

		answer = strings.TrimSpace(answer)
		optionIndex, err := strconv.Atoi(answer)
		if err != nil {
			fmt.Println("Invalid input. Please enter a number.")
			continue
		}

		if optionIndex >= 1 && optionIndex <= len(params.Options) {
			selectedOption := params.Options[optionIndex-1]
			return &acp.RequestPermissionResponse{
				Outcome: acp.NewRequestPermissionOutcomeSelected(selectedOption.OptionID),
			}, nil
		}
		fmt.Printf("Invalid option. Please choose a number between 1 and %d.\n", len(params.Options))
	}
}

func (c *ExampleClient) SessionUpdate(ctx context.Context, params *acp.SessionNotification) error {
	// Use MatchSessionUpdate for exhaustive, type-safe handling
	acp.MatchSessionUpdate(&params.Update, acp.SessionUpdateMatcher[struct{}]{
		AgentMessageChunk: func(v acp.SessionUpdateAgentMessageChunk) struct{} {
			acp.MatchContentBlock(&v.Content, acp.ContentBlockMatcher[struct{}]{
				Text: func(t acp.ContentBlockText) struct{} {
					fmt.Print(t.Text)
					return struct{}{}
				},
				Default: func() struct{} {
					fmt.Print("[non-text content]")
					return struct{}{}
				},
			})
			return struct{}{}
		},
		AgentThoughtChunk: func(v acp.SessionUpdateAgentThoughtChunk) struct{} {
			if text, ok := v.Content.AsText(); ok {
				fmt.Printf("💭 %s", text.Text)
			}
			return struct{}{}
		},
		ToolCall: func(v acp.SessionUpdateToolCall) struct{} {
			fmt.Printf("\n🔧 %s", v.Title)
			if v.Status != nil {
				fmt.Printf(" (%s)", *v.Status)
			}
			fmt.Println()
			return struct{}{}
		},
		ToolCallUpdate: func(v acp.SessionUpdateToolCallUpdate) struct{} {
			fmt.Printf("🔧 Tool `%s` updated", v.ToolCallID)
			if v.Status != nil {
				fmt.Printf(": %s", *v.Status)
			}
			fmt.Println()
			return struct{}{}
		},
		Plan: func(_ acp.SessionUpdatePlan) struct{} {
			fmt.Println("[plan update]")
			return struct{}{}
		},
		Default: func() struct{} { return struct{}{} },
	})

	return nil
}

func (c *ExampleClient) WriteTextFile(ctx context.Context, params *acp.WriteTextFileRequest) (*acp.WriteTextFileResponse, error) {
	return &acp.WriteTextFileResponse{}, nil
}

func (c *ExampleClient) ReadTextFile(ctx context.Context, params *acp.ReadTextFileRequest) (*acp.ReadTextFileResponse, error) {
	return &acp.ReadTextFileResponse{Content: "Mock file content"}, nil
}

func (c *ExampleClient) CreateTerminal(ctx context.Context, params *acp.CreateTerminalRequest) (*acp.CreateTerminalResponse, error) {
	return &acp.CreateTerminalResponse{TerminalID: "mock-terminal-id"}, nil
}

func (c *ExampleClient) TerminalOutput(ctx context.Context, params *acp.TerminalOutputRequest) (*acp.TerminalOutputResponse, error) {
	return &acp.TerminalOutputResponse{Output: "Mock terminal output"}, nil
}

func (c *ExampleClient) ReleaseTerminal(ctx context.Context, params *acp.ReleaseTerminalRequest) (*acp.ReleaseTerminalResponse, error) {
	return &acp.ReleaseTerminalResponse{}, nil
}

func (c *ExampleClient) WaitForTerminalExit(ctx context.Context, params *acp.WaitForTerminalExitRequest) (*acp.WaitForTerminalExitResponse, error) {
	return &acp.WaitForTerminalExitResponse{}, nil
}

func (c *ExampleClient) KillTerminalCommand(ctx context.Context, params *acp.KillTerminalRequest) (*acp.KillTerminalResponse, error) {
	return &acp.KillTerminalResponse{}, nil
}

func main() {
	ctx := context.Background()

	// Build the agent binary
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		fmt.Fprintf(os.Stderr, "Failed to get current file path\n")
		os.Exit(1)
	}

	currentDir := filepath.Dir(currentFile)
	exampleDir := filepath.Dir(currentDir)
	agentDir := filepath.Join(exampleDir, "agent")

	agentBinary := filepath.Join(agentDir, "agent")
	if runtime.GOOS == "windows" {
		agentBinary += ".exe"
	}

	if _, err := os.Stat(agentBinary); os.IsNotExist(err) {
		fmt.Println("Building agent...")
		buildCmd := exec.Command("go", "build", "-o", agentBinary, "main.go")
		buildCmd.Dir = agentDir
		buildCmd.Stderr = os.Stderr
		if err := buildCmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to build agent: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Agent built successfully.")
	}

	// Spawn the agent using the helper
	client := &ExampleClient{}
	connection, err := acp.SpawnAgent(ctx, client, agentBinary)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to spawn agent: %v\n", err)
		os.Exit(1)
	}

	// Start the connection in background
	go func() {
		if err := connection.Start(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Connection error: %v\n", err)
		}
	}()

	// Initialize the connection
	initResult, err := connection.Initialize(ctx, &acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersion(acp.CurrentProtocolVersion),
		ClientCapabilities: &acp.ClientCapabilities{
			FS: &acp.FileSystemCapabilities{
				ReadTextFile:  true,
				WriteTextFile: true,
			},
			Terminal: false,
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Connected to agent (protocol v%d)\n", initResult.ProtocolVersion)

	// Create a new session
	cwd, _ := os.Getwd()
	sessionResult, err := connection.NewSession(ctx, &acp.NewSessionRequest{
		Cwd:        cwd,
		MCPServers: []acp.MCPServer{},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create session: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Created session: %s\n", sessionResult.SessionID)
	fmt.Printf("User: Hello, agent!\n\n")

	// Send a test prompt
	promptResult, err := connection.Prompt(ctx, &acp.PromptRequest{
		SessionID: sessionResult.SessionID,
		Prompt: []acp.ContentBlock{
			acp.NewContentBlockText("Hello, agent!"),
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to send prompt: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n\nAgent completed with: %s\n", promptResult.StopReason)
}
