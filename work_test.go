package acp_test

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"slices"
	"testing"

	acp "github.com/ironpark/acp-go"
	"github.com/ironpark/acp-go/internal/jsonrpc"
)

func TestWorkTrackerCancelsOnlyItsKey(t *testing.T) {
	var tracker acp.WorkTracker[string]
	a, doneA := tracker.Track(t.Context(), "a")
	b, doneB := tracker.Track(t.Context(), "b")
	defer doneB()

	if !tracker.Cancel("a") {
		t.Fatal("Cancel(a) found nothing")
	}
	if !acp.WorkCancelled(a) || b.Err() != nil {
		t.Fatalf("a cancelled %v, b err %v", acp.WorkCancelled(a), b.Err())
	}
	doneA()
	if tracker.Cancel("a") {
		t.Error("Cancel after done reported tracked work")
	}
	if tracker.Cancel("missing") {
		t.Error("Cancel of an unknown key reported tracked work")
	}
}

func TestWorkTrackerFollowsParent(t *testing.T) {
	var tracker acp.WorkTracker[int]
	parent, cancel := context.WithCancel(t.Context())
	ctx, done := tracker.Track(parent, 1)
	defer done()
	cancel()
	<-ctx.Done()
	if acp.WorkCancelled(ctx) {
		t.Error("a cancelled parent read as work cancelled by the client")
	}
}

func TestWorkTrackerReplacedEntry(t *testing.T) {
	var tracker acp.WorkTracker[string]
	_, doneOld := tracker.Track(t.Context(), "k")
	newer, doneNew := tracker.Track(t.Context(), "k")
	defer doneNew()
	doneOld() // must not untrack the newer entry
	if !tracker.Cancel("k") || !acp.WorkCancelled(newer) {
		t.Error("the newer entry is no longer tracked")
	}
}

func TestSessionNotFoundRoundTrip(t *testing.T) {
	local := acp.SessionNotFound("s1")
	if !acp.IsCode(local, acp.ErrorCodeResourceNotFound) {
		t.Fatalf("code = %d", local.Code)
	}
	if id, ok := acp.SessionNotFoundID(local); !ok || id != "s1" {
		t.Errorf("local: %q %v", id, ok)
	}
	// As received from the peer: raw JSON data.
	_, message, data := jsonrpc.ErrorObject(local)
	received := jsonrpc.FromErrorObject(acp.ErrorCodeResourceNotFound, message, data)
	if id, ok := acp.SessionNotFoundID(received); !ok || id != "s1" {
		t.Errorf("received: %q %v", id, ok)
	}
	// A resource-not-found error for a file is not a session's.
	file := acp.ResourceNotFound("/x").WithData(map[string]string{"uri": "/x"})
	if _, ok := acp.SessionNotFoundID(file); ok {
		t.Error("a file error read as a missing session")
	}
	if _, ok := acp.SessionNotFoundID(errors.New("plain")); ok {
		t.Error("a plain error read as a missing session")
	}
}

func TestSupportedProtocolVersions(t *testing.T) {
	err := acp.UnsupportedProtocolVersion(1, 2, 3)
	if versions, ok := acp.SupportedProtocolVersions(err); !ok || !slices.Equal(versions, []uint16{2, 3}) {
		t.Errorf("local: %v %v", versions, ok)
	}
	peer := jsonrpc.FromErrorObject(acp.ErrorCodeUnsupportedProtocolVersion, "Unsupported protocol version",
		jsontext.Value(`{"requested":1,"supported":[2]}`))
	if versions, ok := acp.SupportedProtocolVersions(peer); !ok || !slices.Equal(versions, []uint16{2}) {
		t.Errorf("received: %v %v", versions, ok)
	}
}
