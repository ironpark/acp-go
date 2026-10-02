package acpconn

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"

	"github.com/ironpark/acp-go/internal/jsonrpc"
)

// emptyParams stands in for an omitted params member so that requests whose
// fields are all optional still validate.
var emptyParams = jsontext.Value(`{}`)

// decodeParams decodes raw into a T, applying the schema version's Zod rules
// carried by validated. Failures become -32602, as in the TypeScript SDK.
func decodeParams[T any](validated json.Options, raw jsontext.Value) (*T, error) {
	if len(raw) == 0 {
		raw = emptyParams
	}
	params := new(T)
	if err := json.Unmarshal(raw, params, validated); err != nil {
		return nil, jsonrpc.InvalidParams(err.Error())
	}
	return params, nil
}

// Request decodes params and invokes a typed request handler. A nil response
// is encoded as an empty object, matching the reference SDKs' void methods.
func Request[T, R any](ctx context.Context, validated json.Options, raw jsontext.Value, fn func(context.Context, *T) (*R, error)) (any, error) {
	params, err := decodeParams[T](validated, raw)
	if err != nil {
		return nil, err
	}
	response, err := fn(ctx, params)
	if err != nil {
		return nil, err
	}
	if response == nil {
		return nil, nil
	}
	return response, nil
}

// Notify decodes params and invokes a typed notification handler.
func Notify[T any](ctx context.Context, validated json.Options, raw jsontext.Value, fn func(context.Context, *T) error) error {
	params, err := decodeParams[T](validated, raw)
	if err != nil {
		return err
	}
	return fn(ctx, params)
}

// Call sends a request and decodes its response. A null or empty result
// decodes to the zero value, which is how the protocol spells "void".
//
// Responses are not validated: the peer is the authority on its own output,
// and the reference SDKs do not validate them either.
func Call[R any](ctx context.Context, conn *jsonrpc.Connection, method string, params any) (*R, error) {
	raw, err := conn.SendRequest(ctx, method, params)
	if err != nil {
		return nil, err
	}
	return DecodeResult[R](raw)
}

// CallUntimed is [Call] for a request that waits for the user, such as a
// permission request: the connection's request timeout does not bound it,
// only ctx does.
func CallUntimed[R any](ctx context.Context, conn *jsonrpc.Connection, method string, params any) (*R, error) {
	return Call[R](jsonrpc.WithoutTimeout(ctx), conn, method, params)
}

// StartCall sends a request like [Call] but returns once it is queued, with a
// function that waits for and decodes the response. A message sent after
// StartCall returns reaches the peer after the request.
func StartCall[R any](ctx context.Context, conn *jsonrpc.Connection, method string, params any) (func() (*R, error), error) {
	wait, err := conn.StartRequest(ctx, method, params)
	if err != nil {
		return nil, err
	}
	return func() (*R, error) {
		raw, err := wait()
		if err != nil {
			return nil, err
		}
		return DecodeResult[R](raw)
	}, nil
}

// DecodeResult decodes a response result, treating null or empty as the zero
// value.
func DecodeResult[R any](raw jsontext.Value) (*R, error) {
	response := new(R)
	if len(raw) == 0 || string(raw) == "null" {
		return response, nil
	}
	if err := json.Unmarshal(raw, response); err != nil {
		return nil, err
	}
	return response, nil
}
