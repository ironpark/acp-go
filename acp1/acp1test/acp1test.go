// Package acp1test helps test ACP v1 agents: [Client] is a client that records
// what an agent sends and answers its permission requests, and [Connect]
// runs an agent against it in memory.
package acp1test

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/ironpark/acp-go"
	"github.com/ironpark/acp-go/acp1"
)

// Client is a acp1.Client for tests. It records every session update and
// permission request it receives and answers the requests with Permission.
// Its zero value is ready to use and allows every request once. It is safe
// for concurrent use.
type Client struct {
	// Permission answers a permission request. When nil, the client answers
	// with [AllowOnce].
	Permission func(*acp1.RequestPermissionRequest) *acp1.RequestPermissionResponse

	mu          sync.Mutex
	updates     []*acp1.SessionNotification
	permissions []*acp1.RequestPermissionRequest
	changed     chan struct{} // closed and cleared on every update
}

var _ acp1.Client = (*Client)(nil)

// SessionUpdate records the update.
func (c *Client) SessionUpdate(_ context.Context, params *acp1.SessionNotification) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.updates = append(c.updates, params)
	if c.changed != nil {
		close(c.changed)
		c.changed = nil
	}
	return nil
}

// RequestPermission records the request and answers it with Permission.
func (c *Client) RequestPermission(_ context.Context, params *acp1.RequestPermissionRequest) (*acp1.RequestPermissionResponse, error) {
	c.mu.Lock()
	c.permissions = append(c.permissions, params)
	answer := c.Permission
	c.mu.Unlock()
	if answer == nil {
		answer = AllowOnce
	}
	return answer(params), nil
}

// AllowOnce answers a permission request with its first allow-once option,
// or its first allow-always one, and cancels a request that offers neither.
func AllowOnce(params *acp1.RequestPermissionRequest) *acp1.RequestPermissionResponse {
	return choose(params, acp1.PermissionOptionKindAllowOnce, acp1.PermissionOptionKindAllowAlways)
}

// AllowAlways answers a permission request with its first allow-always
// option, or its first allow-once one, and cancels a request that offers
// neither.
func AllowAlways(params *acp1.RequestPermissionRequest) *acp1.RequestPermissionResponse {
	return choose(params, acp1.PermissionOptionKindAllowAlways, acp1.PermissionOptionKindAllowOnce)
}

// Reject answers a permission request with its first reject-once option, or
// its first reject-always one, and cancels a request that offers neither.
func Reject(params *acp1.RequestPermissionRequest) *acp1.RequestPermissionResponse {
	return choose(params, acp1.PermissionOptionKindRejectOnce, acp1.PermissionOptionKindRejectAlways)
}

// choose selects the first option of the first kind offered.
func choose(params *acp1.RequestPermissionRequest, kinds ...acp1.PermissionOptionKind) *acp1.RequestPermissionResponse {
	for _, kind := range kinds {
		for _, option := range params.Options {
			if option.Kind == kind {
				return acp1.PermissionSelected(option.OptionID)
			}
		}
	}
	return acp1.PermissionCancelled()
}

// Updates returns the session updates received so far, in order.
func (c *Client) Updates() []*acp1.SessionNotification {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.updates)
}

// Permissions returns the permission requests received so far, in order.
func (c *Client) Permissions() []*acp1.RequestPermissionRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.permissions)
}

// Text returns the text of the agent message chunks the session streamed so
// far, joined.
func (c *Client) Text(id acp1.SessionID) string {
	var b strings.Builder
	for _, update := range c.Updates() {
		if update.SessionID != id {
			continue
		}
		if chunk, ok := update.Update.As[acp1.SessionUpdateAgentMessageChunk](); ok {
			if text, ok := acp1.TextOf(chunk.Content); ok {
				b.WriteString(text)
			}
		}
	}
	return b.String()
}

// WaitFor returns the first update, received so far or later, that match
// accepts, or ctx's error once ctx is done. match sees each update once, in
// order, and may call the client's methods. It suits updates an agent sends
// outside a prompt turn, such as the commands that follow session/new, which
// a test cannot otherwise tell when to expect.
func (c *Client) WaitFor(ctx context.Context, match func(*acp1.SessionNotification) bool) (*acp1.SessionNotification, error) {
	seen := 0
	for {
		c.mu.Lock()
		updates := c.updates[seen:]
		if c.changed == nil {
			c.changed = make(chan struct{})
		}
		changed := c.changed
		c.mu.Unlock()
		// match runs without the lock, so it may call the client's methods.
		for _, update := range updates {
			if match(update) {
				return update, nil
			}
		}
		seen += len(updates)
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// Connect runs the agent newAgent builds against client over an in-memory
// pipe and returns the client's side of the connection, not yet initialized.
// Both sides stop when the test ends.
func Connect(tb testing.TB, newAgent func(*acp1.AgentSideConnection) acp1.Agent, client acp1.Client, opts ...acp.Option) *acp1.ClientSideConnection {
	tb.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	agentConn, clientConn := acp1.Pipe(ctx, newAgent, func(*acp1.ClientSideConnection) acp1.Client { return client }, opts...)
	tb.Cleanup(func() {
		cancel()
		for _, done := range []<-chan struct{}{agentConn.Done(), clientConn.Done()} {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				tb.Error("acp1test: the connection did not stop")
			}
		}
	})
	return clientConn
}
