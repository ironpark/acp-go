// Command agent is a complete ACP agent over stdio.
//
// It builds on the echo example with the pieces a real agent needs: session
// state and turn cancellation through an embedded acp1.SessionManager,
// session modes (modes.go), a plan and tool calls streamed with
// acp1.SessionStream (tools.go), a command run in the client's terminal, a
// file diff, a permission request before a destructive tool call, a typed
// extension method through acp.ExtRouter, and request logging through
// middleware.
package main

import (
	"context"
	"log/slog"
	"os"
	"sync"

	acp "github.com/ironpark/acp-go"
	"github.com/ironpark/acp-go/acp1"
)

// session holds the state the agent keeps per ACP session.
type session struct {
	cwd string

	mu   sync.Mutex
	mode acp1.SessionModeID // changed by session/set_mode while a turn may run
}

// exampleAgent embeds a SessionManager, which supplies NewSession and CancelSession
// plus the optional session/delete, session/resume and session/close handlers,
// and an ExtRouter, which serves the extension methods registered on it.
// It needs no credentials, so it leaves out Authenticate.
type exampleAgent struct {
	*acp1.SessionManager[*session]
	acp.ExtRouter
	client *acp1.AgentSideConnection
}

// Extension methods start with an underscore and a domain the agent owns.
const pingMethod = "_example.com/ping"

type pingParams struct {
	Message string `json:"message"`
}

type pingResult struct {
	Reply string `json:"reply"`
}

func (a *exampleAgent) ping(_ context.Context, params *pingParams) (*pingResult, error) {
	return &pingResult{Reply: "pong: " + params.Message}, nil
}

func (a *exampleAgent) Initialize(context.Context, *acp1.InitializeRequest) (*acp1.InitializeResponse, error) {
	// CapabilitiesOf advertises exactly the optional methods implemented.
	return &acp1.InitializeResponse{
		ProtocolVersion:   acp1.ProtocolVersion,
		AgentCapabilities: acp1.CapabilitiesOf(a),
		AgentInfo:         &acp1.Implementation{Name: "example-agent", Version: "0.1.0"},
	}, nil
}

// Prompt runs the turn through the embedded manager, whose CancelSession cancels the
// turn's context; a cancelled turn is answered as cancelled.
func (a *exampleAgent) Prompt(ctx context.Context, params *acp1.PromptRequest) (*acp1.PromptResponse, error) {
	return a.RunTurn(ctx, params.SessionID, func(ctx context.Context, sess *session) (acp1.StopReason, error) {
		// JoinTexts skips images, resources and other non-text blocks.
		return acp1.StopReasonEndTurn, a.runTurn(ctx, params.SessionID, sess, acp1.JoinTexts(params.Prompt))
	})
}

// newManager returns the session manager: sessions in memory, starting in
// ask mode.
func newManager() *acp1.SessionManager[*session] {
	return acp1.NewSessionManager(
		acp1.NewMemoryStore[*session](),
		func(_ context.Context, params *acp1.NewSessionRequest) (acp1.SessionID, *session, error) {
			return acp1.GenerateSessionID(), &session{cwd: params.Cwd, mode: askMode}, nil
		},
	)
}

// newAgent returns the constructor of a connection's agent; the agents
// share manager's sessions.
func newAgent(manager *acp1.SessionManager[*session]) func(*acp1.AgentSideConnection) acp1.Agent {
	return func(c *acp1.AgentSideConnection) acp1.Agent {
		a := &exampleAgent{SessionManager: manager, client: c}
		a.HandleExt(pingMethod, a.ping)
		return a
	}
}

func main() {
	// Stdout carries the protocol, so logs go to stderr.
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	conn := acp1.NewAgentSideConnection(newAgent(newManager()), acp.NewStdioTransport(os.Stdin, os.Stdout),
		acp.WithMiddleware(acp.LoggingMiddleware(logger)),
		acp.WithErrorHandler(func(err error) { logger.Error("acp", "error", err) }),
	)

	if err := conn.Start(context.Background()); err != nil {
		logger.Error("connection ended", "error", err)
		os.Exit(1)
	}
}
