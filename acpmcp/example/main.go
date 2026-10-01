// Command example hands an agent an MCP server that lives in the client, over
// the ACP connection itself (MCP-over-ACP, an unstable draft):
//
//	cd acpmcp && go run ./example
//
// The client registers an MCP server with a word_count tool and lists it in
// session/new with the "acp" transport. The agent connects to it with the MCP
// Go SDK and calls the tool to answer each prompt. Both sides run in this
// process, joined by acp1.Pipe; over stdio or HTTP the code is the same.
package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	acp "github.com/ironpark/acp-go"
	"github.com/ironpark/acp-go/acp1"
	"github.com/ironpark/acp-go/acpmcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// agent connects to the ACP-transport MCP servers of each new session and
// uses their word_count tool. The embedded DialerV1 carries the servers'
// messages back and makes CapabilitiesOf advertise mcpCapabilities.acp.
type agent struct {
	*acpmcp.DialerV1
	client acp1.Client

	mu    sync.Mutex
	tools map[acp1.SessionID]*mcp.ClientSession
}

func (a *agent) Initialize(context.Context, *acp1.InitializeRequest) (*acp1.InitializeResponse, error) {
	return &acp1.InitializeResponse{ProtocolVersion: acp1.ProtocolVersion, AgentCapabilities: acp1.CapabilitiesOf(a)}, nil
}

func (a *agent) NewSession(ctx context.Context, params *acp1.NewSessionRequest) (*acp1.NewSessionResponse, error) {
	id := acp1.GenerateSessionID()
	for _, server := range params.MCPServers {
		entry, ok := server.As[acp1.MCPServerACP]()
		if !ok {
			continue // stdio and HTTP servers are reached the usual way
		}
		session, err := a.Connect(ctx, entry, mcp.NewClient(&mcp.Implementation{Name: "example-agent", Version: "0.1.0"}, nil), nil)
		if err != nil {
			return nil, fmt.Errorf("connect to MCP server %s: %w", entry.Name, err)
		}
		a.mu.Lock()
		a.tools[id] = session
		a.mu.Unlock()
	}
	return &acp1.NewSessionResponse{SessionID: id}, nil
}

func (a *agent) Prompt(ctx context.Context, params *acp1.PromptRequest) (*acp1.PromptResponse, error) {
	a.mu.Lock()
	tools := a.tools[params.SessionID]
	a.mu.Unlock()
	if tools == nil {
		return nil, acp.InvalidParams("this session has no MCP server")
	}
	text := acp1.JoinTexts(params.Prompt)
	result, err := tools.CallTool(ctx, &mcp.CallToolParams{Name: "word_count", Arguments: map[string]any{"text": text}})
	if err != nil {
		return nil, err
	}
	reply := fmt.Sprintf("The client's word_count tool says: %v words.", result.StructuredContent.(map[string]any)["words"])
	if err := acp1.NewSessionStream(a.client, params.SessionID).SendText(ctx, reply); err != nil {
		return nil, err
	}
	return &acp1.PromptResponse{StopReason: acp1.StopReasonEndTurn}, nil
}

func (a *agent) CancelSession(context.Context, *acp1.CancelNotification) error { return nil }

// client provides MCP servers through the embedded HostV1, which answers the
// agent's mcp/message requests. The turns read their
// own updates, so UnimplementedClient covers the rest.
type client struct {
	acp1.UnimplementedClient
	*acpmcp.HostV1
}

type countInput struct {
	Text string `json:"text" jsonschema:"the text to count"`
}

type countOutput struct {
	Words int `json:"words"`
}

// newTools is the MCP server the client provides, written with the MCP Go
// SDK as for any other transport.
func newTools() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "client-tools", Version: "0.1.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "word_count", Description: "Counts the words in a text"},
		func(_ context.Context, _ *mcp.CallToolRequest, in countInput) (*mcp.CallToolResult, countOutput, error) {
			return nil, countOutput{Words: len(strings.Fields(in.Text))}, nil
		})
	return server
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var c *client
	_, conn := acp1.Pipe(ctx,
		func(conn *acp1.AgentSideConnection) acp1.Agent {
			return &agent{DialerV1: acpmcp.NewDialerV1(conn), client: conn, tools: map[acp1.SessionID]*mcp.ClientSession{}}
		},
		func(conn *acp1.ClientSideConnection) acp1.Client {
			c = &client{HostV1: acpmcp.NewHostV1(conn)}
			return c
		})

	initialized, err := conn.Initialize(ctx, &acp1.InitializeRequest{})
	if err != nil {
		log.Fatal(err)
	}
	// Offer the server only to an agent that can reach it over ACP.
	if !initialized.GetAgentCapabilities().GetMCPCapabilities().GetACP() {
		log.Fatal("the agent does not support MCP-over-ACP")
	}
	session, err := conn.StartSession(ctx, &acp1.NewSessionRequest{
		Cwd:        "/",
		MCPServers: []acp1.MCPServer{c.Add("client-tools", newTools())},
	})
	if err != nil {
		log.Fatal(err)
	}
	for _, prompt := range []string{"hello there", "MCP tools can live in the client"} {
		turn, err := session.Prompt(ctx, acp1.TextBlock(prompt))
		if err != nil {
			log.Fatal(err)
		}
		text, _, err := turn.Text()
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf(">> %s\n<< %s\n", prompt, text)
	}
}
