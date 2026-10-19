package acpconn

import (
	"slices"
	"testing"
)

func TestTurnBuffersUpdatesForALateReader(t *testing.T) {
	turn := NewTurn[int, string]()
	turn.Push(1)
	turn.Push(2)
	turn.Finish("done", nil)
	turn.Push(3) // dropped: the turn has ended

	var got []int
	for u := range turn.Updates() {
		got = append(got, u)
	}
	if !slices.Equal(got, []int{1, 2}) {
		t.Fatalf("Updates yielded %v, want [1 2]", got)
	}
	if result, err := turn.Wait(); result != "done" || err != nil {
		t.Fatalf("Wait = %q, %v", result, err)
	}
	select {
	case <-turn.Done():
	default:
		t.Fatal("Done is not closed after the turn ended")
	}
}

func TestTurnStreamsToAWaitingReader(t *testing.T) {
	turn := NewTurn[int, string]()
	done := make(chan []int, 1)
	go func() {
		var got []int
		for u := range turn.Updates() {
			got = append(got, u)
		}
		done <- got
	}()
	turn.Push(7)
	turn.Finish("x", nil)
	if got := <-done; !slices.Equal(got, []int{7}) {
		t.Fatalf("Updates yielded %v, want [7]", got)
	}
}

func TestFinishIsIdempotent(t *testing.T) {
	turn := NewTurn[int, string]()
	turn.Finish("first", nil)
	turn.Finish("second", errRejected) // ignored
	if result, err := turn.Wait(); result != "first" || err != nil {
		t.Fatalf("Wait = %q, %v; want the first result", result, err)
	}
}

func TestDeliverOnlyReachesAnActiveSession(t *testing.T) {
	var turns Turns[string, int, string]
	turns.Deliver("s", 1) // no turn: dropped
	turn, err := turns.Begin("s")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := turns.Begin("s"); err != ErrTurnInProgress {
		t.Fatalf("second Begin = %v, want ErrTurnInProgress", err)
	}
	turns.Deliver("s", 5)
	turns.End("s", turn, "stop", nil)
	turns.Deliver("s", 6) // after End: dropped
	var got []int
	for u := range turn.Updates() {
		got = append(got, u)
	}
	if len(got) != 1 || got[0] != 5 {
		t.Fatalf("updates = %v, want [5]", got)
	}
	if result, err := turn.Wait(); result != "stop" || err != nil {
		t.Fatalf("Wait = %q, %v", result, err)
	}
}
