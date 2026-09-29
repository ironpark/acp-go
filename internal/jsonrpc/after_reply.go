package jsonrpc

import (
	"context"
	"fmt"
	"sync"
)

// replyHooksKey carries a request's [replyHooks] in its handler context.
type replyHooksKey struct{}

// replyHooks collects the functions a request handler registered with
// [AfterReply], to run once its response is queued.
type replyHooks struct {
	mu   sync.Mutex
	fns  []func(context.Context)
	done bool // the response went out or the request failed
}

// AfterReply registers fn to run once the request whose handler context ctx
// derives from has been answered successfully; the root package's AfterReply
// documents the contract. fn runs after the response is queued on the
// connection, whose writes go out in order, so what fn sends cannot overtake
// the response.
func AfterReply(ctx context.Context, fn func(context.Context)) bool {
	hooks, ok := ctx.Value(replyHooksKey{}).(*replyHooks)
	if !ok {
		return false
	}
	hooks.mu.Lock()
	defer hooks.mu.Unlock()
	if hooks.done {
		return false
	}
	hooks.fns = append(hooks.fns, fn)
	return true
}

// withReplyHooks returns a request context that [AfterReply] can register on.
func withReplyHooks(ctx context.Context) (context.Context, *replyHooks) {
	hooks := &replyHooks{}
	return context.WithValue(ctx, replyHooksKey{}, hooks), hooks
}

// take closes the hooks to further registrations and returns them.
func (h *replyHooks) take() []func(context.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.done = true
	fns := h.fns
	h.fns = nil
	return fns
}

// runReplyHooks runs the hooks of an answered request, reporting a panic in
// one to the error handler without skipping the rest.
func (c *Connection) runReplyHooks(ctx context.Context, method string, fns []func(context.Context)) {
	ctx = context.WithoutCancel(ctx)
	for _, fn := range fns {
		func() {
			defer func() {
				if r := recover(); r != nil {
					c.logError(fmt.Errorf("panic in after-reply hook for %s: %v", method, r))
				}
			}()
			fn(ctx)
		}()
	}
}
