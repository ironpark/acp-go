package jsonrpc

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"strings"
	"sync"
	"testing"
	"time"
)

// blockingNotifications returns a notification handler that blocks until
// release is called or the connection stops, reporting on entered when a call
// starts.
func blockingNotifications(t *testing.T) (handler NotificationHandler, entered <-chan string, release func()) {
	t.Helper()
	gate := make(chan struct{})
	seen := make(chan string, 16)
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	return func(ctx context.Context, method string, _ jsontext.Value) error {
		seen <- method
		select {
		case <-gate:
		case <-ctx.Done():
		}
		return nil
	}, seen, release
}

func waitEntered(t *testing.T, entered <-chan string, want string) {
	t.Helper()
	select {
	case got := <-entered:
		if got != want {
			t.Fatalf("notification handler entered %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("notification handler never entered %q", want)
	}
}

// receiveWithin returns the next outgoing message, or nil if none is written
// within d.
func (t *pipeTransport) receiveWithin(tb testing.TB, d time.Duration) map[string]jsontext.Value {
	tb.Helper()
	select {
	case data := <-t.out:
		var msg map[string]jsontext.Value
		if err := json.Unmarshal(data, &msg); err != nil {
			tb.Fatalf("decode written message %s: %v", data, err)
		}
		return msg
	case <-time.After(d):
		return nil
	}
}

// A response must not reach its caller before the notifications the peer sent
// ahead of it have been handled: a client reading a prompt result expects every
// session/update of that turn to be applied already.
func TestResponseWaitsForEarlierNotifications(t *testing.T) {
	transport := newPipeTransport()
	handler, entered, release := blockingNotifications(t)
	conn := start(t, nil, handler, transport)

	done := make(chan error, 1)
	go func() {
		_, err := conn.SendRequest(t.Context(), "session/prompt", nil)
		done <- err
	}()
	id := transport.receive(t)["id"]

	transport.in <- jsontext.Value(`{"jsonrpc":"2.0","method":"session/update"}`)
	waitEntered(t, entered, "session/update")
	transport.in <- jsontext.Value(`{"jsonrpc":"2.0","id":` + string(id) + `,"result":{}}`)

	select {
	case err := <-done:
		t.Fatalf("SendRequest returned (%v) before the earlier notification was handled", err)
	case <-time.After(200 * time.Millisecond):
	}

	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SendRequest: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SendRequest never returned after the notification was handled")
	}
}

// A request's handler starts only once the notifications read before it have
// been handled: an agent answering nes/suggest must already have applied the
// document/didChange sent ahead of it.
func TestRequestWaitsForEarlierNotifications(t *testing.T) {
	transport := newPipeTransport()
	handler, entered, release := blockingNotifications(t)
	called := make(chan struct{}, 1)
	start(t, func(context.Context, string, jsontext.Value) (any, error) {
		called <- struct{}{}
		return nil, nil
	}, handler, transport)

	transport.in <- jsontext.Value(`{"jsonrpc":"2.0","method":"document/didChange"}`)
	waitEntered(t, entered, "document/didChange")
	transport.in <- jsontext.Value(`{"jsonrpc":"2.0","id":1,"method":"nes/suggest"}`)

	select {
	case <-called:
		t.Fatal("request handler started before the earlier notification was handled")
	case <-time.After(200 * time.Millisecond):
	}
	release()
	if msg := transport.receive(t); string(msg["id"]) != "1" {
		t.Fatalf("answered id %s, want 1", msg["id"])
	}
}

// A request waiting behind a slow notification has already been read, so the
// peer can still cancel it.
func TestRequestWaitingBehindSlowNotificationCanBeCancelled(t *testing.T) {
	transport := newPipeTransport()
	handler, entered, _ := blockingNotifications(t)
	start(t, func(context.Context, string, jsontext.Value) (any, error) {
		t.Error("request handler ran for a request cancelled before it started")
		return nil, nil
	}, handler, transport)

	transport.in <- jsontext.Value(`{"jsonrpc":"2.0","method":"session/update"}`)
	waitEntered(t, entered, "session/update")
	transport.in <- jsontext.Value(`{"jsonrpc":"2.0","id":1,"method":"session/request_permission"}`)
	transport.in <- jsontext.Value(`{"jsonrpc":"2.0","method":"$/cancel_request","params":{"requestId":1}}`)

	msg := transport.receiveWithin(t, 500*time.Millisecond)
	if msg == nil {
		t.Fatal("the waiting request was not cancelled while a notification handler was running")
	}
	var wire wireError
	if err := json.Unmarshal(msg["error"], &wire); err != nil {
		t.Fatalf("decode error object: %v", err)
	}
	if wire.Code != CodeRequestCancelled {
		t.Errorf("code = %d, want %d", wire.Code, CodeRequestCancelled)
	}
}

// A notification handler may call the peer and wait for the answer: the
// response is read while the handler runs, and it does not wait for the
// notification that is making the call.
func TestNotificationHandlerCanCallThePeer(t *testing.T) {
	transport := newPipeTransport()
	var conn *Connection
	result := make(chan error, 1)
	conn = start(t, nil, func(ctx context.Context, _ string, _ jsontext.Value) error {
		_, err := conn.SendRequest(ctx, "session/set_mode", nil)
		result <- err
		return err
	}, transport)

	transport.in <- jsontext.Value(`{"jsonrpc":"2.0","method":"session/update"}`)
	request := transport.receive(t)
	// A later notification is queued behind the running one; the response
	// must not wait for it either.
	transport.in <- jsontext.Value(`{"jsonrpc":"2.0","method":"later"}`)
	transport.in <- jsontext.Value(`{"jsonrpc":"2.0","id":` + string(request["id"]) + `,"result":{}}`)

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("SendRequest from a notification handler: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a notification handler calling the peer deadlocked")
	}
}

// Notifications read before EOF are all handled before Start returns.
func TestNotificationsReadBeforeEOFAreHandled(t *testing.T) {
	transport := newPipeTransport()
	var handled []string
	conn := New(nil, func(_ context.Context, method string, _ jsontext.Value) error {
		time.Sleep(10 * time.Millisecond)
		handled = append(handled, method)
		return nil
	}, transport)

	for _, method := range []string{"first", "second", "third"} {
		transport.in <- jsontext.Value(`{"jsonrpc":"2.0","method":"` + method + `"}`)
	}
	transport.Close()
	if err := conn.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := strings.Join(handled, ","); got != "first,second,third" {
		t.Fatalf("handled %s before Start returned", got)
	}
}

// $/cancel_request must take effect even while a notification handler is
// running; cancellation is exactly what a peer sends when things are slow.
func TestSlowNotificationDoesNotDelayCancelRequest(t *testing.T) {
	transport := newPipeTransport()
	handler, entered, _ := blockingNotifications(t)
	started := make(chan struct{})
	start(t, func(ctx context.Context, _ string, _ jsontext.Value) (any, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}, handler, transport)

	transport.in <- jsontext.Value(`{"jsonrpc":"2.0","id":1,"method":"slow"}`)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("request handler never started")
	}
	transport.in <- jsontext.Value(`{"jsonrpc":"2.0","method":"session/update"}`)
	waitEntered(t, entered, "session/update")
	transport.in <- jsontext.Value(`{"jsonrpc":"2.0","method":"$/cancel_request","params":{"requestId":1}}`)

	msg := transport.receiveWithin(t, 500*time.Millisecond)
	if msg == nil {
		t.Fatal("$/cancel_request did not reach the request while a notification handler was running")
	}
	var wire wireError
	if err := json.Unmarshal(msg["error"], &wire); err != nil {
		t.Fatalf("decode error object: %v", err)
	}
	if wire.Code != CodeRequestCancelled {
		t.Errorf("code = %d, want %d", wire.Code, CodeRequestCancelled)
	}
}

// Notifications stay ordered among themselves even when they are handled off
// the read loop.
func TestSlowNotificationKeepsLaterNotificationsOrdered(t *testing.T) {
	transport := newPipeTransport()
	order := make(chan string, 8)
	gate := make(chan struct{})
	start(t, nil, func(ctx context.Context, method string, _ jsontext.Value) error {
		if method == "first" {
			select {
			case <-gate:
			case <-ctx.Done():
			}
		}
		order <- method
		return nil
	}, transport)

	for _, method := range []string{"first", "second", "third"} {
		transport.in <- jsontext.Value(`{"jsonrpc":"2.0","method":"` + method + `"}`)
	}
	time.Sleep(50 * time.Millisecond)
	close(gate)
	for _, want := range []string{"first", "second", "third"} {
		select {
		case got := <-order:
			if got != want {
				t.Fatalf("handled %q, want %q", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for %q", want)
		}
	}
}
