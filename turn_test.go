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

// A cancelled turn keeps its session until done: the next turn waits for the
// cancelled work to end rather than running beside it.
func TestTurnTrackerCancelledTurnHoldsTheSession(t *testing.T) {
	var turns TurnTracker[string]
	first, done, err := turns.Begin(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !turns.Cancel("s1") || !turns.Cancel("s1") {
		t.Fatal("Cancel did not find the turn, the second time too")
	}
	if !TurnCancelled(first) {
		t.Fatalf("cause = %v", context.Cause(first))
	}

	// Begin waits, and gives up with its context.
	short, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	if _, _, err := turns.Begin(short, "s1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Begin behind a cancelled turn: %v, want its context's error", err)
	}

	for name, begin := range map[string]func() (context.Context, func()){
		"Begin": func() (context.Context, func()) {
			ctx, done, err := turns.Begin(context.Background(), "s1")
			if err != nil {
				t.Errorf("Begin: %v", err)
			}
			return ctx, done
		},
		"Join": func() (context.Context, func()) {
			ctx, done, joined := turns.Join(context.Background(), "s1")
			if joined {
				t.Error("Join joined a cancelled turn")
			}
			return ctx, done
		},
	} {
		type next struct {
			ctx  context.Context
			done func()
		}
		started := make(chan next)
		go func() {
			ctx, nextDone := begin()
			started <- next{ctx, nextDone}
		}()
		select {
		case <-started:
			t.Fatalf("%s started a turn beside the cancelled one", name)
		case <-time.After(30 * time.Millisecond):
		}
		done() // the cancelled work has ended
		select {
		case n := <-started:
			if n.ctx == nil || n.ctx.Err() != nil {
				t.Fatalf("%s: the next turn did not start live", name)
			}
			// The next round waits behind this turn, cancelled in its place.
			done = n.done
			turns.Cancel("s1")
		case <-time.After(2 * time.Second):
			t.Fatalf("%s never started after the cancelled turn ended", name)
		}
	}
	done()
}

// A cancelled turn settles even with a joined prompt unanswered: its work
// can answer nothing more.
func TestTurnTrackerSettleAfterCancel(t *testing.T) {
	var turns TurnTracker[string]
	_, done, _ := turns.Join(context.Background(), "s1")
	defer done()
	turns.Join(context.Background(), "s1")
	turns.Cancel("s1")
	if !turns.Settle("s1") {
		t.Fatal("a cancelled turn did not settle")
	}
}
