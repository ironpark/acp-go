package acpconn

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"testing"

	"github.com/ironpark/acp-go/internal/jsonrpc"
	schema "github.com/ironpark/acp-go/schema/v1"
)

type sampleParams struct {
	Name string `json:"name"`
}

type sampleResult struct {
	Echo string `json:"echo"`
}

func TestDecodeParams(t *testing.T) {
	params, err := decodeParams[sampleParams](schema.Validated(), jsontext.Value(`{"name":"x"}`))
	if err != nil || params.Name != "x" {
		t.Fatalf("decodeParams = %+v, %v", params, err)
	}

	// An omitted or null params member stands in as an empty object, so
	// all-optional requests validate and required members are still enforced.
	for _, raw := range []jsontext.Value{nil, jsontext.Value("null")} {
		if _, err := decodeParams[schema.ListSessionsRequest](schema.Validated(), raw); err != nil {
			t.Errorf("decodeParams(%q) for session/list = %v", raw, err)
		}
		if _, err := decodeParams[schema.NewSessionRequest](schema.Validated(), raw); !isInvalidParams(err) {
			t.Errorf("decodeParams(%q) for session/new = %v, want invalid params", raw, err)
		}
	}

	// A type mismatch is an invalid-params error, not a decode panic.
	_, err = decodeParams[sampleParams](schema.Validated(), jsontext.Value(`{"name":123}`))
	if !isInvalidParams(err) {
		t.Fatalf("decodeParams(mismatch) = %v, want invalid params", err)
	}
}

func TestRequest(t *testing.T) {
	ctx := t.Context()
	var seen string
	out, err := Request[sampleParams, sampleResult](ctx, schema.Validated(), jsontext.Value(`{"name":"hi"}`),
		func(_ context.Context, p *sampleParams) (*sampleResult, error) {
			seen = p.Name
			return &sampleResult{Echo: p.Name}, nil
		})
	if err != nil || seen != "hi" {
		t.Fatalf("Request = %#v, %v (handler saw %q)", out, err, seen)
	}
	if result, ok := out.(*sampleResult); !ok || result.Echo != "hi" {
		t.Fatalf("Request returned %#v", out)
	}

	// A nil response is passed through so the connection encodes an empty object.
	out, err = Request[sampleParams, sampleResult](ctx, schema.Validated(), jsontext.Value(`{}`),
		func(context.Context, *sampleParams) (*sampleResult, error) { return nil, nil })
	if err != nil || out != nil {
		t.Fatalf("Request(nil response) = %#v, %v", out, err)
	}

	// A handler error passes through unchanged.
	sentinel := errors.New("boom")
	_, err = Request[sampleParams, sampleResult](ctx, schema.Validated(), jsontext.Value(`{}`),
		func(context.Context, *sampleParams) (*sampleResult, error) { return nil, sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("Request(handler error) = %v", err)
	}
}

func TestNotify(t *testing.T) {
	ctx := t.Context()
	var seen string
	err := Notify(ctx, schema.Validated(), jsontext.Value(`{"name":"n"}`),
		func(_ context.Context, p *sampleParams) error {
			seen = p.Name
			return nil
		})
	if err != nil || seen != "n" {
		t.Fatalf("Notify = %v (handler saw %q)", err, seen)
	}

	// Invalid params fail before the handler runs.
	called := false
	err = Notify(ctx, schema.Validated(), jsontext.Value(`{"name":123}`),
		func(context.Context, *sampleParams) error { called = true; return nil })
	if !isInvalidParams(err) {
		t.Fatalf("Notify(mismatch) = %v, want invalid params", err)
	}
	if called {
		t.Fatal("Notify ran the handler for invalid params")
	}
}

func TestDecodeResult(t *testing.T) {
	// Null and empty results decode to the zero value, the way the protocol
	// spells "void".
	for _, raw := range []jsontext.Value{nil, jsontext.Value("null")} {
		got, err := DecodeResult[sampleResult](raw)
		if err != nil || got == nil || *got != (sampleResult{}) {
			t.Fatalf("DecodeResult(%q) = %+v, %v", raw, got, err)
		}
	}
	got, err := DecodeResult[sampleResult](jsontext.Value(`{"echo":"y"}`))
	if err != nil || got.Echo != "y" {
		t.Fatalf("DecodeResult = %+v, %v", got, err)
	}
	if _, err := DecodeResult[sampleResult](jsontext.Value(`{`)); err == nil {
		t.Fatal("DecodeResult of malformed JSON should fail")
	}
}

func TestCallAndStartCall(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var client *jsonrpc.Connection
	Pipe(ctx, func(tr jsonrpc.Transport) Conn {
		return jsonrpc.New(func(_ context.Context, method string, _ jsontext.Value) (any, error) {
			return sampleResult{Echo: method}, nil
		}, nil, tr)
	}, func(tr jsonrpc.Transport) Conn {
		client = jsonrpc.New(nil, nil, tr)
		return client
	})

	got, err := Call[sampleResult](ctx, client, "x/y", nil)
	if err != nil || got.Echo != "x/y" {
		t.Fatalf("Call = %+v, %v", got, err)
	}

	wait, err := StartCall[sampleResult](ctx, client, "a/b", sampleParams{Name: "n"})
	if err != nil {
		t.Fatal(err)
	}
	started, err := wait()
	if err != nil || started.Echo != "a/b" {
		t.Fatalf("StartCall = %+v, %v", started, err)
	}
}
