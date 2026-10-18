package acpconn

import (
	"context"
	"sync"
)

// Queue buffers a session's updates for a single reader, independently of any
// request. The buffer is unbounded so that the connection's read loop, which
// pushes updates, never waits on the reader.
type Queue[U any] struct {
	mu      sync.Mutex
	items   []U
	closed  bool
	err     error         // what reads fail with once closed
	signal  chan struct{} // wakes a reader waiting for an update
	done    <-chan struct{}
	doneErr error
}

// NewQueue returns an empty queue that closes with doneErr once done is
// closed.
func NewQueue[U any](done <-chan struct{}, doneErr error) *Queue[U] {
	return &Queue[U]{signal: make(chan struct{}, 1), done: done, doneErr: doneErr}
}

// Push appends an update. Updates pushed after Close are dropped.
func (q *Queue[U]) Push(u U) {
	q.mu.Lock()
	if !q.closed {
		q.items = append(q.items, u)
	}
	q.mu.Unlock()
	select {
	case q.signal <- struct{}{}:
	default:
	}
}

// Close stops accepting updates; those already queued can still be read,
// after which reads fail with err. Only the first call has an effect.
func (q *Queue[U]) Close(err error) {
	q.mu.Lock()
	if !q.closed {
		q.closed, q.err = true, err
	}
	q.mu.Unlock()
	select {
	case q.signal <- struct{}{}:
	default:
	}
}

// Next returns the oldest queued update, waiting for one if needed. A
// cancelled ctx ends the wait without discarding anything; once the queue is
// closed or done, Next fails with the queue's error after the last update.
func (q *Queue[U]) Next(ctx context.Context) (U, error) {
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			u := q.items[0]
			var zero U
			q.items[0] = zero
			q.items = q.items[1:]
			q.mu.Unlock()
			return u, nil
		}
		closed, err := q.closed, q.err
		q.mu.Unlock()
		var zero U
		if closed {
			return zero, err
		}
		select {
		case <-q.signal:
		case <-q.done:
			q.Close(q.doneErr)
		case <-ctx.Done():
			return zero, context.Cause(ctx)
		}
	}
}
