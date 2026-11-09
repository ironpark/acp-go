package acp

import (
	"context"
	"errors"
	"sync"
)

// ErrWorkCancelled is the cause of a context [WorkTracker.Cancel] cancelled:
// the client asked to stop one piece of work, such as a v2 tool call with
// tool_call/cancel or a terminal with terminal/cancel, rather than the whole
// turn. [WorkCancelled] checks for it.
var ErrWorkCancelled = errors.New("acp: work cancelled by the client")

// WorkCancelled reports whether ctx, or the work context it derives from, was
// cancelled through [WorkTracker.Cancel], as opposed to the turn being
// cancelled ([TurnCancelled]), a deadline or the connection closing.
func WorkCancelled(ctx context.Context) bool {
	return errors.Is(context.Cause(ctx), ErrWorkCancelled)
}

// WorkTracker holds a cancellable context for each piece of work in progress,
// keyed by K, so a request from the client can stop one piece without
// stopping the turn it belongs to. The zero value is ready to use and it is
// safe for concurrent use.
//
// Derive the work's context from the turn's, so cancelling the turn stops the
// work too:
//
//	ctx, done := tracker.Track(turnCtx, key)
//	defer done()
//	err := run(ctx)
//	if acp.WorkCancelled(ctx) {
//		// report the work cancelled, with a context that is not
//	}
type WorkTracker[K comparable] struct {
	mu   sync.Mutex
	work map[K]*trackedWork
}

type trackedWork struct {
	cancel context.CancelCauseFunc
}

// Track registers the work key names and returns its context, derived from
// ctx, and a done func to call when the work ends; until then
// [WorkTracker.Cancel] can cancel the context. Tracking a key that is still
// tracked replaces the earlier entry, which then can no longer be cancelled
// by key.
func (t *WorkTracker[K]) Track(ctx context.Context, key K) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(ctx)
	entry := &trackedWork{cancel: cancel}
	t.mu.Lock()
	if t.work == nil {
		t.work = map[K]*trackedWork{}
	}
	t.work[key] = entry
	t.mu.Unlock()
	return ctx, sync.OnceFunc(func() {
		t.mu.Lock()
		if t.work[key] == entry {
			delete(t.work, key)
		}
		t.mu.Unlock()
		cancel(nil)
	})
}

// Cancel cancels the tracked work key names with [ErrWorkCancelled] and
// reports whether there was any. The work stays tracked until its done func
// runs, so a second Cancel also reports true; the protocol answers both the
// same way.
func (t *WorkTracker[K]) Cancel(key K) bool {
	t.mu.Lock()
	entry := t.work[key]
	t.mu.Unlock()
	if entry != nil {
		entry.cancel(ErrWorkCancelled)
	}
	return entry != nil
}
