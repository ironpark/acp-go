package acp

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTurnTrackerBegin(t *testing.T) {
	var turns TurnTracker[string]
	ctx, done, err := turns.Begin(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := turns.Begin(context.Background(), "s1"); !errors.Is(err, ErrTurnInProgress) {
		t.Fatalf("second Begin: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("a refused Begin must not disturb the running turn")
	}
	if !turns.Cancel("s1") || context.Cause(ctx) != ErrTurnCancelled {
		t.Fatalf("cause = %v", context.Cause(ctx))
	}
	done()
	if turns.Cancel("s1") {
		t.Fatal("cancelled a finished turn")
	}
	if _, done, err := turns.Begin(context.Background(), "s1"); err != nil {
		t.Fatalf("session not freed: %v", err)
	} else {
		done()
	}
}

func TestTurnTrackerJoin(t *testing.T) {
	var turns TurnTracker[string]
	first, done, joined := turns.Join(context.Background(), "s")
	if joined {
		t.Fatal("first Join should start the turn")
	}
	second, noop, joined := turns.Join(context.Background(), "s")
	if !joined || second != first {
		t.Fatal("second Join should return the running turn")
	}
	noop()
	if first.Err() != nil {
		t.Fatal("a joined caller's done must not end the turn")
	}
	done()
	if first.Err() == nil {
		t.Fatal("the starter's done should end the turn")
	}
}

// TestTurnTrackerSettle: a turn settles only once no caller joined since the
// last try, and a caller arriving after that waits for the end and starts a
// new turn instead of joining one that no longer reads prompts.
func TestTurnTrackerSettle(t *testing.T) {
	var turns TurnTracker[string]
	first, done, _ := turns.Join(context.Background(), "s1")
	if !turns.Settle("s1") {
		t.Fatal("a turn nobody joined did not settle")
	}
	done()

	_, done, _ = turns.Join(context.Background(), "s1")
	if _, _, joined := turns.Join(context.Background(), "s1"); !joined {
		t.Fatal("the second caller did not join")
	}
	if turns.Settle("s1") {
		t.Fatal("settled with a joined caller not answered")
	}
	if !turns.Settle("s1") {
		t.Fatal("did not settle once the joined caller was answered")
	}

	started := make(chan bool)
	go func() {
		_, laterDone, joined := turns.Join(context.Background(), "s1")
		defer laterDone()
		started <- joined
	}()
	select {
	case <-started:
		t.Fatal("a caller joined a settled turn")
	case <-time.After(50 * time.Millisecond):
	}
	done()
	if joined := <-started; joined {
		t.Error("the caller after the end joined instead of starting a turn")
	}
	if first.Err() == nil {
		t.Error("the first turn's context is still live")
	}
}
