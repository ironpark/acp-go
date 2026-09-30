package jsonrpc

import (
	"context"
	"sync"
)

// runningNotificationKey carries the sequence number of the notification
// whose handler a context belongs to.
type runningNotificationKey struct{}

type queuedNotification struct {
	seq uint64 // 1 for the first notification read
	msg wireMessage
}

// notificationQueue hands incoming notifications from the read loop to the
// notification loop and tracks how far handling has got. It is unbounded, so
// the read loop never waits on a handler.
type notificationQueue struct {
	mu      sync.Mutex
	pending []queuedNotification
	closed  bool // the read loop has ended; drain pending, then stop
	wake    chan struct{}

	// received counts the notifications queued so far. Only the read loop
	// writes it, under mu, so the read loop may read it without mu.
	received uint64
	// handled counts the notifications whose handler has returned.
	handled uint64
	// advanced is closed when handled grows; nil until someone waits.
	advanced chan struct{}
}

func (q *notificationQueue) push(msg wireMessage) {
	q.mu.Lock()
	q.received++
	q.pending = append(q.pending, queuedNotification{seq: q.received, msg: msg})
	q.mu.Unlock()
	q.signal()
}

// close lets the notification loop stop once the queue is drained.
func (q *notificationQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.signal()
}

func (q *notificationQueue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// next returns the next notification to handle, or false once the queue is
// closed and drained or ctx is done.
func (q *notificationQueue) next(ctx context.Context) (queuedNotification, bool) {
	for {
		if ctx.Err() != nil {
			return queuedNotification{}, false
		}
		q.mu.Lock()
		if len(q.pending) > 0 {
			n := q.pending[0]
			q.pending[0] = queuedNotification{}
			q.pending = q.pending[1:]
			q.mu.Unlock()
			return n, true
		}
		closed := q.closed
		q.mu.Unlock()
		if closed {
			return queuedNotification{}, false
		}
		select {
		case <-q.wake:
		case <-ctx.Done():
		}
	}
}

// done records that the handler of notification seq has returned.
func (q *notificationQueue) done(seq uint64) {
	q.mu.Lock()
	q.handled = seq
	if q.advanced != nil {
		close(q.advanced)
		q.advanced = nil
	}
	q.mu.Unlock()
}

// handledThrough reports whether the first n notifications have been handled,
// and if not, returns a channel closed when handling next advances.
func (q *notificationQueue) handledThrough(n uint64) (<-chan struct{}, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.handled >= n {
		return nil, true
	}
	if q.advanced == nil {
		q.advanced = make(chan struct{})
	}
	return q.advanced, false
}

// notificationLoop handles queued notifications one at a time, in arrival
// order. Once the connection is cancelled the ones not yet started are
// dropped; after EOF they are all handled.
func (c *Connection) notificationLoop() {
	for {
		n, ok := c.notifications.next(c.ctx)
		if !ok {
			return
		}
		c.handleNotification(n)
		c.notifications.done(n.seq)
	}
}

// awaitNotifications waits until the first n incoming notifications have
// been handled.
//
// Called from a notification handler, it does not wait for that notification
// or any after it: they cannot be handled before the handler returns, so a
// handler may call the peer and wait for the answer.
func (c *Connection) awaitNotifications(ctx context.Context, n uint64) error {
	if running, ok := ctx.Value(runningNotificationKey{}).(uint64); ok && running <= n {
		n = running - 1
	}
	if n == 0 {
		return nil
	}
	for {
		advanced, ok := c.notifications.handledThrough(n)
		if ok {
			return nil
		}
		select {
		case <-advanced:
		case <-ctx.Done():
			return ctx.Err()
		case <-c.ctx.Done():
			return context.Cause(c.ctx)
		}
	}
}
