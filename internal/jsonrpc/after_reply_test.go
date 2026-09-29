package jsonrpc

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"testing"
)

func TestAfterReplySendsAfterTheResponse(t *testing.T) {
	transport := newPipeTransport()
	var conn *Connection
	conn = start(t, func(ctx context.Context, method string, _ jsontext.Value) (any, error) {
		for _, name := range []string{"first", "second"} {
			if !AfterReply(ctx, func(ctx context.Context) {
				if ctx.Err() != nil {
					t.Errorf("hook context is cancelled: %v", ctx.Err())
				}
				_ = conn.SendNotification(ctx, name, nil)
			}) {
				t.Error("AfterReply refused a request context")
			}
		}
		if method == "fail" {
			return nil, errors.New("failed")
		}
		return map[string]string{"ok": "yes"}, nil
	}, nil, transport)

	transport.in <- jsontext.Value(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if got := transport.receive(t); string(got["id"]) != "1" {
		t.Fatalf("first message = %v, want the response", got)
	}
	for _, want := range []string{`"first"`, `"second"`} {
		if got := transport.receive(t); string(got["method"]) != want {
			t.Fatalf("next message = %v, want notification %s", got, want)
		}
	}

	// A failed request runs no hooks: its error is the next and only message.
	transport.in <- jsontext.Value(`{"jsonrpc":"2.0","id":2,"method":"fail"}`)
	if got := transport.receive(t); string(got["id"]) != "2" || got["error"] == nil {
		t.Fatalf("message = %v, want the error response", got)
	}
	transport.in <- jsontext.Value(`{"jsonrpc":"2.0","id":3,"method":"ping"}`)
	if got := transport.receive(t); string(got["id"]) != "3" {
		t.Fatalf("message = %v, want the response to the next request", got)
	}
}

func TestAfterReplyOutsideARequest(t *testing.T) {
	if AfterReply(t.Context(), func(context.Context) { t.Error("hook ran") }) {
		t.Error("AfterReply accepted a context that is not a request's")
	}
}

func TestAfterReplyRefusedOnceAnswered(t *testing.T) {
	transport := newPipeTransport()
	late := make(chan context.Context, 1)
	start(t, func(ctx context.Context, _ string, _ jsontext.Value) (any, error) {
		late <- ctx
		return nil, nil
	}, nil, transport)

	transport.in <- jsontext.Value(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	transport.receive(t)
	if AfterReply(<-late, func(context.Context) { t.Error("hook ran") }) {
		t.Error("AfterReply accepted a hook after the response")
	}
}
