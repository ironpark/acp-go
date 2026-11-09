package acp1

import (
	"context"
	"slices"

	"github.com/ironpark/acp-go/internal/acpconn"
	schema "github.com/ironpark/acp-go/schema/v1"
)

// AuthStatus returns the agent's authentication status as this connection
// last saw it: the answer to the latest [ClientSideConnection.GetAuthStatus]
// or the latest auth/status_update notification, whichever arrived last, or
// nil before either. Only State is meant to drive behavior; treat a state you
// do not recognize as [AuthStateUnknown]. The value is shared, so do not
// modify it.
//
// The notification is recorded whether or not the client implements
// [AuthStatusListener].
func (c *ClientSideConnection) AuthStatus() *AuthStatusResponse { return c.authStatus.Load() }

// getAuthStatus sends auth/status and records the answer. The generated
// [ClientSideConnection.GetAuthStatus] goes through it.
func (c *ClientSideConnection) getAuthStatus(ctx context.Context, params *AuthStatusRequest) (*AuthStatusResponse, error) {
	response, err := acpconn.Call[AuthStatusResponse](ctx, c.conn, schema.AgentMethodsAuthStatus, params)
	if err == nil && response != nil {
		c.recordAuthStatus(response.State, response.Accounts, response.Meta)
	}
	return response, err
}

// authStatusUpdate records an auth/status_update and hands it to the client
// when it is an [AuthStatusListener]. The generated dispatch routes the
// notification here.
func (c *ClientSideConnection) authStatusUpdate(ctx context.Context, n *AuthStatusUpdateNotification) error {
	c.recordAuthStatus(n.State, n.Accounts, n.Meta)
	if h, ok := c.client.(AuthStatusListener); ok {
		return h.AuthStatusUpdate(ctx, n)
	}
	return nil
}

// recordAuthStatus stores a copy of a reported status for AuthStatus, so the
// caller's later changes to its accounts do not reach it.
func (c *ClientSideConnection) recordAuthStatus(state AuthState, accounts []AuthAccount, meta Meta) {
	c.authStatus.Store(&AuthStatusResponse{State: state, Accounts: slices.Clone(accounts), Meta: meta})
}
