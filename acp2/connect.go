package acp2

import (
	"context"

	acp "github.com/ironpark/acp-go"
	"github.com/ironpark/acp-go/internal/acpconn"
)

// RemoteAgent is a connection to an agent outside this process: a child
// process from [SpawnAgent], or a transport such as Streamable HTTP from
// [ConnectAgent]. It embeds the connection, so every agent method is called
// on it directly.
type RemoteAgent struct {
	*ClientSideConnection
	wait func() error
}

// ConnectAgent connects to an agent over transport and starts the read loop:
//
//	agent := acp2.ConnectAgent(ctx, acphttp.NewClientTransport("https://host/acp"), newClient)
//	defer agent.Close()
//	init, err := agent.Initialize(ctx, &acp2.InitializeRequest{})
//
// The agent owns transport from then on: it is closed when the connection
// stops, which for Streamable HTTP deletes the connection on the server.
// Cancelling ctx stops the connection too.
func ConnectAgent(ctx context.Context, transport acp.Transport, newClient func(*ClientSideConnection) Client, opts ...acp.Option) *RemoteAgent {
	conn := NewClientSideConnection(newClient, transport, opts...)
	return &RemoteAgent{ClientSideConnection: conn, wait: acpconn.Run(ctx, conn, transport)}
}

// Close stops the connection and returns once it has stopped, whichever way
// the agent was reached. For [SpawnAgent] that closes the agent's stdin and
// waits for the process to exit, killing it after [ExitGrace]; for
// [ConnectAgent] it closes the transport once queued messages are written, or
// after [ExitGrace] if the agent stops reading them, which for Streamable
// HTTP deletes the connection on the server. [RemoteAgent.Wait] reports how
// it ended.
func (a *RemoteAgent) Close() error {
	err := a.ClientSideConnection.Close()
	_ = a.wait()
	return err
}

// ExitGrace is how long a spawned agent has to exit on its own, once its
// connection stops, before it is killed; and how long a connected agent has
// to take the messages still queued before its transport is closed.
const ExitGrace = acpconn.ExitGrace

// Wait blocks until the connection has stopped: for [SpawnAgent] until the
// process has exited too, for [ConnectAgent] until the transport is closed.
// It reports why, or nil after Close of a connected agent.
func (a *RemoteAgent) Wait() error { return a.wait() }
