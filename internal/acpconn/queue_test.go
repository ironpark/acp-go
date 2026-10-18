package acpconn

import (
	"context"
	"errors"
	"testing"
	"time"
)

var errRejected = errors.New("rejected")

func TestQueueReadsInOrderAcrossWaits(t *testing.T) {
	q := NewQueue[int](nil, nil)
	q.Push(1)
	q.Push(2)
	go func() {
		time.Sleep(10 * time.Millisecond)
		q.Push(3)
	}()
	for want := 1; want <= 3; want++ {
		if got, err := q.Next(t.Context()); err != nil || got != want {
			t.Fatalf("Next = %d, %v; want %d", got, err, want)
		}
	}
}

func TestQueueCancelledReadKeepsUpdates(t *testing.T) {
	q := NewQueue[int](nil, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := q.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Next(cancelled) = %v", err)
	}
	q.Push(7)
	if got, err := q.Next(ctx); err != nil || got != 7 {
		t.Fatalf("a queued update should be read even with a cancelled context: %d, %v", got, err)
	}
}

func TestQueueDrainsBeforeFailing(t *testing.T) {
	done := make(chan struct{})
	q := NewQueue[int](done, errRejected)
	q.Push(1)
	close(done)
	if got, err := q.Next(t.Context()); err != nil || got != 1 {
		t.Fatalf("Next = %d, %v; want the queued update first", got, err)
	}
	if _, err := q.Next(t.Context()); !errors.Is(err, errRejected) {
		t.Fatalf("Next after done = %v, want %v", err, errRejected)
	}
	q.Push(2) // dropped once closed
	if _, err := q.Next(t.Context()); !errors.Is(err, errRejected) {
		t.Fatalf("Next after a late push = %v", err)
	}
}

func TestQueueCloseKeepsTheFirstError(t *testing.T) {
	q := NewQueue[int](nil, nil)
	q.Close(errRejected)
	q.Close(errors.New("second"))
	if _, err := q.Next(t.Context()); !errors.Is(err, errRejected) {
		t.Fatalf("Next = %v, want the first Close error", err)
	}
}
