package acpconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/ironpark/acp-go/internal/jsonrpc"
)

// Conn is the lifecycle surface both façades' connections share.
type Conn interface {
	Start(ctx context.Context) error
	Close() error
	Done() <-chan struct{}
}

// Spawn starts cmd, connects to its stdio through connect and starts the
// connection's read loop. The process is killed when ctx is done, and the
// connection stops once the process exits. On Unix the process gets its own
// process group, so a terminal's Ctrl-C reaches only the client.
//
// Once the connection stops, the process gets ExitGrace to exit before it is
// killed. The returned wait blocks until both have stopped. It reports ctx's error if
// ctx ended the process, otherwise the process's exit error, otherwise the
// read loop's error.
func Spawn(ctx context.Context, cmd *exec.Cmd, connect func(jsonrpc.Transport) Conn) (wait func() error, err error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("agent stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("agent stdout pipe: %w", err)
	}
	if cmd.Stderr == nil {
		// An agent reports startup failures on stderr; dropping it would
		// leave the caller with nothing but a closed connection.
		cmd.Stderr = os.Stderr
	}
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start agent process: %w", err)
	}

	conn := connect(jsonrpc.NewStdioTransport(stdout, stdin))
	stopKill := context.AfterFunc(ctx, func() { _ = cmd.Process.Kill() })
	// Closing the agent's stdin is how a stdio agent learns the client is
	// gone; it exits, and the read loop then sees EOF on its stdout. One that
	// does not is killed.
	exited := make(chan struct{})
	grace := exitGrace
	go func() {
		<-conn.Done()
		_ = stdin.Close()
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case <-exited:
		case <-timer.C:
			_ = cmd.Process.Kill()
		}
	}()

	// Every way out ends the read loop first: the agent exiting or being
	// killed closes its stdout (EOF), and Close reaches it through stdin.
	// cmd.Wait closes the stdout pipe, so it must only run after the loop has
	// read everything the agent wrote.
	done := make(chan error, 1)
	go func() {
		loopErr := conn.Start(ctx)
		processErr := cmd.Wait()
		close(exited)
		stopKill()
		switch {
		case ctx.Err() != nil:
			done <- ctx.Err()
		case processErr != nil:
			done <- fmt.Errorf("agent process: %w", processErr)
		case errors.Is(loopErr, context.Canceled):
			done <- nil // closed by the caller, and the agent exited cleanly
		default:
			done <- loopErr
		}
	}()
	return sync.OnceValue(func() error { return <-done }), nil
}

// ExitGrace is how long a spawned agent has to exit on its own, after its
// connection stops and its stdin closes, before it is killed.
const ExitGrace = 5 * time.Second

// exitGrace is ExitGrace, shortened by tests.
var exitGrace = ExitGrace

// Pipe connects two connections in memory and starts both read loops. When
// either side stops, both pipes close, so the other side reads EOF and stops.
func Pipe(ctx context.Context, agent, client func(jsonrpc.Transport) Conn) {
	toAgentR, toAgentW := io.Pipe()
	toClientR, toClientW := io.Pipe()
	a := agent(jsonrpc.NewStdioTransport(toAgentR, toClientW))
	c := client(jsonrpc.NewStdioTransport(toClientR, toAgentW))
	closeAll := sync.OnceFunc(func() {
		for _, p := range []io.Closer{toAgentR, toAgentW, toClientR, toClientW} {
			_ = p.Close()
		}
	})
	for _, conn := range []Conn{a, c} {
		go func() { _ = conn.Start(ctx) }()
		go func() {
			<-conn.Done()
			closeAll()
		}()
	}
}

// Run starts conn's read loop over transport and returns a wait that blocks
// until it stops. A connection never closes its transport itself, so this is
// where transports the caller dialed end: once conn stops and has flushed its
// writes. That also ends a read that ignores cancellation, as a stdio
// transport's does, which would otherwise keep the read loop running.
func Run(ctx context.Context, conn Conn, transport io.Closer) (wait func() error) {
	closed := make(chan struct{})
	go func() {
		<-conn.Done()
		_ = conn.Close() // returns once queued writes are flushed
		_ = transport.Close()
		close(closed)
	}()
	done := make(chan error, 1)
	go func() {
		err := conn.Start(ctx)
		<-closed
		if errors.Is(err, context.Canceled) && ctx.Err() == nil {
			err = nil // closed by the caller
		}
		done <- err
	}()
	return sync.OnceValue(func() error { return <-done })
}
