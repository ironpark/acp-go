package acpmcp_test

import (
	"context"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"
	"time"

	acp "github.com/ironpark/acp-go"
	"github.com/ironpark/acp-go/acp1"
	"github.com/ironpark/acp-go/acp2"
	"github.com/ironpark/acp-go/acpmcp"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type echoInput struct {
	Message string `json:"message"`
}

type echoOutput struct {
	Reply string `json:"reply"`
}

// newServer is an MCP server with an echo tool that reports progress, and a
// wait tool that runs until it is cancelled, reporting that on cancelled.
func newServer(cancelled chan<- struct{}) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "tools", Version: "1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "Echoes the message"},
		func(ctx context.Context, req *mcp.CallToolRequest, in echoInput) (*mcp.CallToolResult, echoOutput, error) {
			if token := req.Params.GetProgressToken(); token != nil {
				_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: token, Progress: 1, Total: 1})
			}
			return nil, echoOutput{Reply: "echo: " + in.Message}, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "wait", Description: "Waits to be cancelled"},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, struct{}, error) {
			<-ctx.Done()
			cancelled <- struct{}{}
			return nil, struct{}{}, ctx.Err()
		})
	return server
}

// newClient is an MCP client reporting progress and tool list changes.
func newClient(progress chan<- float64, changed chan<- struct{}) *mcp.Client {
	return mcp.NewClient(&mcp.Implementation{Name: "agent", Version: "1.0.0"}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
			progress <- req.Params.Progress
		},
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
			select {
			case changed <- struct{}{}:
			default:
			}
		},
	})
}

// side is one ACP version's way to reach a server the client provides: the
// MCP session the agent opens to it, and a raw mcp/message request.
type side struct {
	connect        func(ctx context.Context, server *mcp.Server, client *mcp.Client) (*mcp.ClientSession, error)
	connectUnknown func(ctx context.Context, client *mcp.Client) error
	// message sends tools/call for name to server under requestID and
	// returns the raw response.
	message func(ctx context.Context, server *mcp.Server, requestID, name string) (jsontext.Value, error)
}

// exercise runs MCP over ACP: requests, an MCP error, progress, a
// subscription's notification, cancellation and the binding's own errors.
func exercise(t *testing.T, s side) {
	ctx := t.Context()
	cancelled := make(chan struct{}, 1)
	server := newServer(cancelled)
	progress := make(chan float64, 1)
	changed := make(chan struct{}, 1)
	session, err := s.connect(ctx, server, newClient(progress, changed))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer session.Close()
	if v := session.InitializeResult().ProtocolVersion; v != "2026-07-28" {
		t.Errorf("negotiated MCP %s, want 2026-07-28", v)
	}

	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 2 {
		t.Fatalf("ListTools: %+v %v", tools, err)
	}
	params := &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"message": "hi"}}
	params.SetProgressToken("p1")
	result, err := session.CallTool(ctx, params)
	if err != nil || result.IsError {
		t.Fatalf("CallTool: %+v %v", result, err)
	}
	if got := result.StructuredContent.(map[string]any)["reply"]; got != "echo: hi" {
		t.Errorf("CallTool = %v, want %q", got, "echo: hi")
	}
	select {
	case p := <-progress:
		if p != 1 {
			t.Errorf("progress = %v", p)
		}
	case <-time.After(2 * time.Second):
		t.Error("the progress notification never reached the agent")
	}

	// An MCP error keeps its code through ACP.
	_, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "missing"})
	if wire, ok := errors.AsType[*jsonrpc.Error](err); !ok || wire.Code != jsonrpc.CodeInvalidParams {
		t.Errorf("calling an unknown tool: %v, want an MCP invalid params error", err)
	}

	// The session's subscription carries the server's list change.
	server.AddTool(&mcp.Tool{Name: "late", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		})
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Error("no tools/list_changed notification reached the agent")
	}

	// Cancelling a call cancels the tool behind it.
	callCtx, cancel := context.WithCancel(ctx)
	called := make(chan error, 1)
	go func() {
		_, err := session.CallTool(callCtx, &mcp.CallToolParams{Name: "wait", Arguments: map[string]any{}})
		called <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelling the call did not cancel the tool")
	}
	if err := <-called; !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled call: %v", err)
	}

	// An MCP error is the response's error outcome, not an ACP error.
	raw, err := s.message(ctx, server, "raw-1", "missing")
	if err != nil {
		t.Fatalf("mcp/message: %v", err)
	}
	var outcome struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := jsonv2.Unmarshal(raw, &outcome); err != nil || outcome.Error == nil || outcome.Error.Code != jsonrpc.CodeInvalidParams {
		t.Errorf("mcp/message for an unknown tool = %s", raw)
	}

	// The binding rejects a request id that is already active.
	go func() { _, _ = s.message(ctx, server, "busy", "wait") }()
	time.Sleep(50 * time.Millisecond)
	if _, err := s.message(ctx, server, "busy", "echo"); !acp.IsCode(err, acp.ErrorCodeInvalidParams) {
		t.Errorf("duplicate active request id: %v, want invalid params", err)
	}

	// A server nobody registered is unavailable.
	if err := s.connectUnknown(ctx, newClient(progress, changed)); err == nil {
		t.Error("connecting to an unknown server succeeded")
	} else if wire, ok := errors.AsType[*jsonrpc.Error](err); !ok || wire.Code != int64(acpmcp.ErrorCodeServerUnavailable) {
		t.Errorf("connecting to an unknown server: %v, want server unavailable", err)
	}
}

type v1Agent struct {
	*acpmcp.DialerV1
}

func (v1Agent) Initialize(context.Context, *acp1.InitializeRequest) (*acp1.InitializeResponse, error) {
	return &acp1.InitializeResponse{ProtocolVersion: acp1.ProtocolVersion}, nil
}
func (v1Agent) NewSession(context.Context, *acp1.NewSessionRequest) (*acp1.NewSessionResponse, error) {
	return &acp1.NewSessionResponse{SessionID: "s"}, nil
}
func (v1Agent) Prompt(context.Context, *acp1.PromptRequest) (*acp1.PromptResponse, error) {
	return &acp1.PromptResponse{StopReason: acp1.StopReasonEndTurn}, nil
}
func (v1Agent) CancelSession(context.Context, *acp1.CancelNotification) error { return nil }

type v1Client struct {
	acp1.UnimplementedClient
	*acpmcp.HostV1
}

func TestMCPOverACPv1(t *testing.T) {
	var agent *v1Agent
	var agentConn *acp1.AgentSideConnection
	var client *v1Client
	acp1.Pipe(t.Context(),
		func(c *acp1.AgentSideConnection) acp1.Agent {
			agentConn, agent = c, &v1Agent{acpmcp.NewDialerV1(c)}
			return agent
		},
		func(c *acp1.ClientSideConnection) acp1.Client {
			client = &v1Client{HostV1: acpmcp.NewHostV1(c)}
			return client
		})

	if !acp1.CapabilitiesOf(agent).GetMCPCapabilities().GetACP() {
		t.Error("an agent embedding DialerV1 does not advertise mcpCapabilities.acp")
	}
	ids := map[*mcp.Server]acp1.MCPServerACPID{}
	entry := func(server *mcp.Server) acp1.MCPServerACP {
		e, ok := client.Add("tools", server).As[acp1.MCPServerACP]() // listed in session/new
		if !ok {
			t.Fatal("Add did not return an acp-transport server")
		}
		ids[server] = e.ServerID
		return e
	}
	exercise(t, side{
		connect: func(ctx context.Context, server *mcp.Server, mcpClient *mcp.Client) (*mcp.ClientSession, error) {
			return agent.Connect(ctx, entry(server), mcpClient, nil)
		},
		connectUnknown: func(ctx context.Context, mcpClient *mcp.Client) error {
			_, err := agent.Connect(ctx, acp1.MCPServerACP{Name: "x", ServerID: "unknown"}, mcpClient, nil)
			return err
		},
		message: func(ctx context.Context, server *mcp.Server, requestID, name string) (jsontext.Value, error) {
			response, err := agentConn.MessageMCP(ctx, &acp1.MessageMCPRequest{
				ServerID: ids[server], RequestID: acp1.MCPRequestID(requestID), Method: "tools/call",
				Params: toolCall(name),
			})
			if err != nil {
				return nil, err
			}
			return response.RawJSON(), nil
		},
	})
}

type v2Agent struct {
	*acpmcp.DialerV2
}

func (v2Agent) Initialize(context.Context, *acp2.InitializeRequest) (*acp2.InitializeResponse, error) {
	return &acp2.InitializeResponse{ProtocolVersion: acp2.ProtocolVersion}, nil
}
func (v2Agent) NewSession(context.Context, *acp2.NewSessionRequest) (*acp2.NewSessionResponse, error) {
	return &acp2.NewSessionResponse{SessionID: "s"}, nil
}
func (v2Agent) Prompt(context.Context, *acp2.PromptRequest) (*acp2.PromptResponse, error) {
	return &acp2.PromptResponse{MessageID: "m"}, nil
}
func (v2Agent) CancelSession(context.Context, *acp2.CancelSessionNotification) error { return nil }

type v2Client struct {
	acp2.UnimplementedClient
	*acpmcp.HostV2
}

func TestMCPOverACPv2(t *testing.T) {
	var agent *v2Agent
	var agentConn *acp2.AgentSideConnection
	var client *v2Client
	acp2.Pipe(t.Context(),
		func(c *acp2.AgentSideConnection) acp2.Agent {
			agentConn, agent = c, &v2Agent{acpmcp.NewDialerV2(c)}
			return agent
		},
		func(c *acp2.ClientSideConnection) acp2.Client {
			client = &v2Client{HostV2: acpmcp.NewHostV2(c)}
			return client
		})

	if acp2.CapabilitiesOf(agent).GetSession().GetMCP().GetACP() == nil {
		t.Error("an agent embedding DialerV2 does not advertise session.mcp.acp")
	}
	ids := map[*mcp.Server]acp2.MCPServerACPID{}
	entry := func(server *mcp.Server) acp2.MCPServerACP {
		e, _ := client.Add("tools", server).As[acp2.MCPServerACP]()
		ids[server] = e.ServerID
		return e
	}
	exercise(t, side{
		connect: func(ctx context.Context, server *mcp.Server, mcpClient *mcp.Client) (*mcp.ClientSession, error) {
			return agent.Connect(ctx, entry(server), mcpClient, nil)
		},
		connectUnknown: func(ctx context.Context, mcpClient *mcp.Client) error {
			_, err := agent.Connect(ctx, acp2.MCPServerACP{Name: "x", ServerID: "unknown"}, mcpClient, nil)
			return err
		},
		message: func(ctx context.Context, server *mcp.Server, requestID, name string) (jsontext.Value, error) {
			response, err := agentConn.MessageMCP(ctx, &acp2.MessageMCPRequest{
				ServerID: ids[server], RequestID: acp2.MCPRequestID(requestID), Method: "tools/call",
				Params: toolCall(name),
			})
			if err != nil {
				return nil, err
			}
			return response.RawJSON(), nil
		},
	})
}

// toolCall is the params of a 2026-07-28 tools/call for name.
func toolCall(name string) map[string]jsontext.Value {
	return map[string]jsontext.Value{
		"name":      jsontext.Value(`"` + name + `"`),
		"arguments": jsontext.Value(`{}`),
		"_meta": jsontext.Value(`{"io.modelcontextprotocol/protocolVersion":"2026-07-28",` +
			`"io.modelcontextprotocol/clientCapabilities":{},"io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"}}`),
	}
}

// TestKeepAliveServer runs a tool on a server that pings its client while
// the tool works: the binding answers the pings, and the call completes.
func TestKeepAliveServer(t *testing.T) {
	var agent *v1Agent
	var client *v1Client
	acp1.Pipe(t.Context(),
		func(c *acp1.AgentSideConnection) acp1.Agent { agent = &v1Agent{acpmcp.NewDialerV1(c)}; return agent },
		func(c *acp1.ClientSideConnection) acp1.Client {
			client = &v1Client{HostV1: acpmcp.NewHostV1(c)}
			return client
		})

	server := mcp.NewServer(&mcp.Implementation{Name: "tools", Version: "1.0.0"}, &mcp.ServerOptions{KeepAlive: 20 * time.Millisecond})
	mcp.AddTool(server, &mcp.Tool{Name: "slow"},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, struct{}, error) {
			time.Sleep(200 * time.Millisecond)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "done"}}}, struct{}{}, nil
		})
	entry, _ := client.Add("tools", server).As[acp1.MCPServerACP]()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	session, err := agent.Connect(ctx, entry, newClient(make(chan float64, 1), make(chan struct{}, 1)), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "slow", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("CallTool: %+v %v", result, err)
	}
}
