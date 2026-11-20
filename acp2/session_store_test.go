package acp2_test

import (
	"context"
	"strings"
	"testing"
	"uuid"

	acp "github.com/ironpark/acp-go"
	"github.com/ironpark/acp-go/acp2"
)

// namedSession is session state that is just a name.
type namedSession string

func (namedSession) SessionInfo() acp2.SessionInfo { return acp2.SessionInfo{Cwd: "/"} }

// bareSession is session state with nothing to it.
type bareSession struct{}

func (bareSession) SessionInfo() acp2.SessionInfo { return acp2.SessionInfo{Cwd: "/"} }

func TestSessionManagerLookup(t *testing.T) {
	manager := acp2.NewSessionManager(acp2.NewMemoryStore[namedSession](),
		func(context.Context, *acp2.NewSessionRequest) (acp2.SessionID, namedSession, error) {
			return "s1", "state", nil
		})
	if _, err := manager.NewSession(t.Context(), &acp2.NewSessionRequest{}); err != nil {
		t.Fatal(err)
	}
	if got, err := manager.Lookup(t.Context(), "s1"); err != nil || got != "state" {
		t.Fatalf("Lookup(s1) = %q, %v", got, err)
	}
	if _, err := manager.Lookup(t.Context(), "nope"); !acp.IsCode(err, acp.ErrorCodeResourceNotFound) {
		t.Fatalf("Lookup(nope) = %v, want resource not found", err)
	}
}

func TestGeneratedIDs(t *testing.T) {
	a, b := acp2.GenerateToolCallID(), acp2.GenerateToolCallID()
	if a == b || !strings.HasPrefix(string(a), "call_") {
		t.Fatalf("tool call ids %q, %q", a, b)
	}
	if id := acp2.GenerateMessageID(); !strings.HasPrefix(string(id), "message_") {
		t.Fatalf("message id %q", id)
	}
	if id := acp2.GenerateCursorID(); !strings.HasPrefix(string(id), "cursor_") {
		t.Fatalf("cursor id %q", id)
	}
	// The UUIDv7 suffix sorts ids in the order they were minted.
	first, second := acp2.GenerateSessionID(), acp2.GenerateSessionID()
	suffix, ok := strings.CutPrefix(string(first), "session_")
	if _, err := uuid.Parse(suffix); !ok || err != nil || first >= second {
		t.Fatalf("session ids %q, %q", first, second)
	}
}
