package acp

import (
	"context"

	"github.com/ironpark/acp-go/internal/jsonrpc"
)

// AfterReply registers fn to run once the request whose handler context ctx
// derives from has been answered successfully. The connection writes its
// messages in order, so what fn sends reaches the peer after the response,
// which is how an agent sends a session update about a session the response
// creates without the update arriving first:
//
//	func (a *myAgent) NewSession(ctx context.Context, params *acp1.NewSessionRequest) (*acp1.NewSessionResponse, error) {
//		id := acp1.GenerateSessionID()
//		acp.AfterReply(ctx, func(ctx context.Context) {
//			acp1.NewSessionStream(a.client, id).SendCommands(ctx, commands)
//		})
//		return &acp1.NewSessionResponse{SessionID: id}, nil
//	}
//
// Functions run in the order they were registered, on the request's goroutine,
// with a context that keeps ctx's values but not its cancellation. They do not
// run when the handler returns an error or the connection closes first.
// AfterReply reports false, and fn never runs, when ctx is not a request
// handler's context or the request has already been answered.
func AfterReply(ctx context.Context, fn func(ctx context.Context)) bool {
	return jsonrpc.AfterReply(ctx, fn)
}
