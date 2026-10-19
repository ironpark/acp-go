package acpconn

import (
	"iter"
	"sync"

	"github.com/ironpark/acp-go/internal/jsonrpc"
)

// ErrTurnInProgress reports a prompt on a session whose previous turn has not
// ended yet. It is an invalid-request error, so an agent can return it to the
// client as is.
var ErrTurnInProgress = jsonrpc.InvalidRequest("session already has a prompt turn in progress")

// Turn buffers the session updates of one prompt turn for a single reader and
// holds the turn's result once it ends. The buffer is unbounded so that the
// connection's read loop, which delivers updates, never waits on the reader.
type Turn[U, R any] struct {
	mu       sync.Mutex
	queue    []U
	finished bool
	result   R
	err      error
	signal   chan struct{} // wakes a reader waiting for updates
	done     chan struct{} // closed once the turn ends
}

// NewTurn returns an empty turn.
func NewTurn[U, R any]() *Turn[U, R] {
	return &Turn[U, R]{signal: make(chan struct{}, 1), done: make(chan struct{})}
}

// Push appends an update. Updates pushed after the turn ends are dropped.
func (t *Turn[U, R]) Push(u U) {
	t.mu.Lock()
	if !t.finished {
		t.queue = append(t.queue, u)
	}
	t.mu.Unlock()
	t.wake()
}

// Finish ends the turn with its result. Only the first call has an effect.
func (t *Turn[U, R]) Finish(result R, err error) {
	t.mu.Lock()
	if t.finished {
		t.mu.Unlock()
		return
	}
	t.finished, t.result, t.err = true, result, err
	t.mu.Unlock()
	close(t.done)
	t.wake()
}

func (t *Turn[U, R]) wake() {
	select {
	case t.signal <- struct{}{}:
	default:
	}
}

// Updates yields the turn's updates in arrival order, including those received
// before the call, and stops once the turn has ended and every update has been
// yielded. It is meant for one reader.
func (t *Turn[U, R]) Updates() iter.Seq[U] {
	return func(yield func(U) bool) {
		for {
			t.mu.Lock()
			batch, finished := t.queue, t.finished
			t.queue = nil
			t.mu.Unlock()
			for _, u := range batch {
				if !yield(u) {
					return
				}
			}
			if len(batch) == 0 {
				if finished {
					return
				}
				<-t.signal
			}
		}
	}
}

// Wait blocks until the turn ends and returns its result.
func (t *Turn[U, R]) Wait() (R, error) {
	<-t.done
	return t.result, t.err
}

// Done is closed once the turn ends.
func (t *Turn[U, R]) Done() <-chan struct{} { return t.done }

// Turns tracks the turn in progress on each session of a v1 client
// connection, where one prompt is one turn.
type Turns[ID comparable, U, R any] struct {
	mu     sync.Mutex
	active map[ID]*Turn[U, R]
}

// Begin registers a new turn for a session, failing with ErrTurnInProgress if
// one is already running. Register before sending the prompt so that no
// update of the turn is missed.
func (r *Turns[ID, U, R]) Begin(id ID) (*Turn[U, R], error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, busy := r.active[id]; busy {
		return nil, ErrTurnInProgress
	}
	if r.active == nil {
		r.active = map[ID]*Turn[U, R]{}
	}
	t := NewTurn[U, R]()
	r.active[id] = t
	return t, nil
}

// Deliver pushes an update to the session's turn in progress, if any.
func (r *Turns[ID, U, R]) Deliver(id ID, u U) {
	r.mu.Lock()
	t := r.active[id]
	r.mu.Unlock()
	if t != nil {
		t.Push(u)
	}
}

// End finishes t and, if it is still the session's turn in progress, frees
// the session for the next prompt.
func (r *Turns[ID, U, R]) End(id ID, t *Turn[U, R], result R, err error) {
	r.mu.Lock()
	if r.active[id] == t {
		delete(r.active, id)
	}
	r.mu.Unlock()
	t.Finish(result, err)
}
