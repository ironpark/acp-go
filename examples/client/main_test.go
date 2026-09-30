package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ironpark/acp-go/acp1"
)

func TestReadTextFileLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &exampleClient{}
	for _, tc := range []struct {
		line, limit *uint32
		want        string
	}{
		{nil, nil, "one\ntwo\nthree\n"},
		{new(uint32(2)), nil, "two\nthree\n"},
		{new(uint32(2)), new(uint32(1)), "two\n"},
		{nil, new(uint32(1)), "one\n"},
		{new(uint32(9)), nil, ""},
	} {
		got, err := c.ReadTextFile(t.Context(), &acp1.ReadTextFileRequest{Path: path, Line: tc.line, Limit: tc.limit})
		if err != nil {
			t.Fatal(err)
		}
		if got.Content != tc.want {
			t.Errorf("line %v limit %v: %q, want %q", tc.line, tc.limit, got.Content, tc.want)
		}
	}
}

// TestPermissionCancelledWithTheTurn: cancelling the turn answers a
// permission request still waiting for input as cancelled.
func TestPermissionCancelledWithTheTurn(t *testing.T) {
	cancelled := make(chan struct{})
	c := &exampleClient{lines: make(chan string), toolTitles: map[acp1.ToolCallID]string{}, cancelled: cancelled}
	close(cancelled)
	response, err := c.RequestPermission(t.Context(), &acp1.RequestPermissionRequest{
		Options: []acp1.PermissionOption{acp1.NewPermissionOption(acp1.PermissionOptionKindAllowOnce, "Allow")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := response.Outcome.As[acp1.RequestPermissionOutcomeCancelled](); !ok {
		t.Errorf("outcome %v, want cancelled", response.Outcome)
	}
}

// TestPermissionWithdrawnByTheAgent: a request the agent cancels stops
// waiting for input.
func TestPermissionWithdrawnByTheAgent(t *testing.T) {
	c := &exampleClient{lines: make(chan string), toolTitles: map[acp1.ToolCallID]string{}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.RequestPermission(ctx, &acp1.RequestPermissionRequest{}); err == nil {
		t.Error("a withdrawn request succeeded")
	}
}
