// Package router serves ACP v1 and the draft v2 on one endpoint.
//
// A [ProtocolRouter] reads the first message of a connection, which must be an
// `initialize` request, and hands the connection to the highest configured
// protocol version that does not exceed the version the client asked for.
// Only the initialize parameters are rewritten; every later message is
// forwarded unchanged. This ports the upstream TypeScript SDK's
// AgentProtocolRouter, with one difference: JSON-RPC batches are not
// supported by this runtime, so a batched initialize is rejected.
//
// A v2 client routed to a v1-only agent receives a v1-shaped initialize
// response carrying protocolVersion 1, exactly as upstream does; the client
// decides what to do with the downgrade. [ClientConnector] is that client
// side: it spawns an agent with v2 and restarts it with v1 when needed.
package router

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"

	acp "github.com/ironpark/acp-go"
	"github.com/ironpark/acp-go/acp1"
	"github.com/ironpark/acp-go/acp2"
	schemav1 "github.com/ironpark/acp-go/schema/v1"
	schemav2 "github.com/ironpark/acp-go/schema/v2"
)

const (
	initializeMethod   = "initialize"
	maxProtocolVersion = 0xffff
)

// ProtocolRouter routes each connection to a v1 or v2 agent implementation.
type ProtocolRouter struct {
	v1   func(*acp1.AgentSideConnection) acp1.Agent
	v2   func(*acp2.AgentSideConnection) acp2.Agent
	opts []acp.Option
}

// New creates a router with no protocol versions configured. opts configure
// the connection of whichever façade a client is routed to.
func New(opts ...acp.Option) *ProtocolRouter { return &ProtocolRouter{opts: opts} }

// WithV1 configures the ACP v1 agent implementation.
func (r *ProtocolRouter) WithV1(newAgent func(*acp1.AgentSideConnection) acp1.Agent) *ProtocolRouter {
	r.v1 = newAgent
	return r
}

// WithV2 configures the draft ACP v2 agent implementation.
func (r *ProtocolRouter) WithV2(newAgent func(*acp2.AgentSideConnection) acp2.Agent) *ProtocolRouter {
	r.v2 = newAgent
	return r
}

// Serve routes one connection and runs it until the peer disconnects or ctx
// is cancelled. Its signature is the one [acphttp.NewServer] takes, so an
// HTTP endpoint serves both versions with acphttp.NewServer(r.Serve).
//
// [acphttp.NewServer]: https://pkg.go.dev/github.com/ironpark/acp-go/acphttp#NewServer
func (r *ProtocolRouter) Serve(ctx context.Context, transport acp.Transport) error {
	first, err := readFirst(ctx, transport)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}

	var msg wireMessage
	if err := json.Unmarshal(first, &msg); err != nil || first.Kind() != '{' {
		// A batch or an undecodable first message: there is no id to answer
		// with, so reject with a null id as upstream does.
		return rejectAndClose(ctx, transport, jsontext.Value("null"),
			acp.InvalidRequest("first ACP message must be an initialize request"))
	}
	if msg.Method == "" || len(msg.ID) == 0 {
		// Notifications and response-shaped messages get no answer.
		return transport.Close()
	}
	if msg.Method != initializeMethod {
		return rejectAndClose(ctx, transport, msg.ID,
			acp.InvalidRequest("first ACP request must be initialize"))
	}

	requested, ok := protocolVersionOf(msg.Params)
	if !ok {
		return rejectAndClose(ctx, transport, msg.ID,
			acp.InvalidParams("initialize.protocolVersion must be a valid ACP protocol version"))
	}
	selected, ok := r.highestCompatible(requested)
	if !ok {
		return rejectAndClose(ctx, transport, msg.ID, acp.InvalidRequest(
			fmt.Sprintf("unsupported ACP protocol version %d; this endpoint supports %s", requested, r.supported())))
	}

	params, err := rewriteInitializeParams(msg.Params, requested, selected)
	if err != nil {
		return rejectAndClose(ctx, transport, msg.ID,
			acp.InvalidParams("invalid initialize params: "+err.Error()))
	}
	msg.Params = params
	rewritten, err := json.Marshal(&msg)
	if err != nil {
		return rejectAndClose(ctx, transport, msg.ID, acp.InternalError(err.Error()))
	}

	routed := &replayTransport{first: rewritten, Transport: transport}
	switch selected {
	case 2:
		return acp2.NewAgentSideConnection(r.v2, routed, r.opts...).Start(ctx)
	default:
		return acp1.NewAgentSideConnection(r.v1, routed, r.opts...).Start(ctx)
	}
}

// highestCompatible picks the newest configured version the client accepts.
func (r *ProtocolRouter) highestCompatible(requested uint64) (int, bool) {
	if r.v2 != nil && requested >= 2 {
		return 2, true
	}
	if r.v1 != nil && requested >= 1 {
		return 1, true
	}
	return 0, false
}

func (r *ProtocolRouter) supported() string {
	switch {
	case r.v1 != nil && r.v2 != nil:
		return "ACP protocol versions 1 and 2"
	case r.v1 != nil:
		return "ACP protocol version 1"
	case r.v2 != nil:
		return "ACP protocol version 2"
	}
	return "no ACP protocol versions"
}

// wireMessage is the subset of a JSON-RPC message the router inspects. Every
// member is kept so the rewritten initialize round-trips intact.
type wireMessage struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      jsontext.Value `json:"id,omitzero"`
	Method  string         `json:"method,omitzero"`
	Params  jsontext.Value `json:"params,omitzero"`
	Result  jsontext.Value `json:"result,omitzero"`
	Error   jsontext.Value `json:"error,omitzero"`
}

// readFirst returns the first non-empty message from the transport.
func readFirst(ctx context.Context, transport acp.Transport) (jsontext.Value, error) {
	for {
		data, err := transport.ReadMessage(ctx)
		if err != nil {
			return nil, err
		}
		if len(data) > 0 {
			return data, nil
		}
	}
}

// protocolVersionOf extracts the protocolVersion of an initialize request or
// response, which must be an integer in [0, 0xffff]. JSON spells integers
// loosely, so 1.0 is accepted.
func protocolVersionOf(params jsontext.Value) (uint64, bool) {
	var payload struct {
		ProtocolVersion *float64 `json:"protocolVersion"`
	}
	if len(params) == 0 || json.Unmarshal(params, &payload) != nil || payload.ProtocolVersion == nil {
		return 0, false
	}
	v := *payload.ProtocolVersion
	if v != math.Trunc(v) || v < 0 || v > maxProtocolVersion {
		return 0, false
	}
	return uint64(v), true
}

// rewriteInitializeParams validates the initialize params in the shape the
// client sent and re-encodes them in the shape the selected version expects.
func rewriteInitializeParams(params jsontext.Value, requested uint64, selected int) (jsontext.Value, error) {
	if selected == 2 {
		var req schemav2.InitializeRequest
		if err := json.Unmarshal(params, &req, schemav2.Validated()); err != nil {
			return nil, err
		}
		req.ProtocolVersion = 2
		return json.Marshal(&req)
	}
	if requested >= 2 {
		var req schemav2.InitializeRequest
		if err := json.Unmarshal(params, &req, schemav2.Validated()); err != nil {
			return nil, err
		}
		v1, err := v2InitializeToV1(&req)
		if err != nil {
			return nil, err
		}
		return json.Marshal(v1)
	}
	var req schemav1.InitializeRequest
	if err := json.Unmarshal(params, &req, schemav1.Validated()); err != nil {
		return nil, err
	}
	req.ProtocolVersion = 1
	return json.Marshal(&req)
}

// v2InitializeToV1 downgrades a v2 initialize request for a v1 agent.
func v2InitializeToV1(req *schemav2.InitializeRequest) (*schemav1.InitializeRequest, error) {
	capabilities, err := v2ClientCapabilitiesToV1(req.Capabilities)
	if err != nil {
		return nil, err
	}
	info, err := convert[schemav1.Implementation](req.Info)
	if err != nil {
		return nil, err
	}
	return &schemav1.InitializeRequest{
		ProtocolVersion:    1,
		ClientCapabilities: capabilities,
		ClientInfo:         &info,
		Meta:               req.Meta,
	}, nil
}

// v2ClientCapabilitiesToV1 maps v2 client capabilities onto v1's. v2 has no
// fs or terminal methods, so those are reported as unsupported; v1-only
// capabilities that v2 takes for granted are reported as supported.
func v2ClientCapabilitiesToV1(capabilities *schemav2.ClientCapabilities) (*schemav1.ClientCapabilities, error) {
	no := false
	result := &schemav1.ClientCapabilities{
		FS:       &schemav1.FileSystemCapabilities{ReadTextFile: &no, WriteTextFile: &no},
		Terminal: &no,
		Session: &schemav1.ClientSessionCapabilities{
			ConfigOptions: &schemav1.SessionConfigOptionsCapabilities{
				Boolean: &schemav1.BooleanConfigOptionCapabilities{},
			},
		},
		Plan: &schemav1.PlanCapabilities{},
	}
	if capabilities == nil {
		result.Auth = &schemav1.AuthCapabilities{Terminal: &no}
		return result, nil
	}

	auth, err := v2AuthCapabilitiesToV1(capabilities.Auth)
	if err != nil {
		return nil, err
	}
	result.Auth = auth
	if capabilities.Elicitation != nil {
		elicitation, err := convert[schemav1.ElicitationCapabilities](capabilities.Elicitation)
		if err != nil {
			return nil, err
		}
		result.Elicitation = &elicitation
	}
	result.Meta = capabilities.Meta
	return result, nil
}

// v2AuthCapabilitiesToV1 collapses v2's terminal auth object into v1's flag.
func v2AuthCapabilitiesToV1(capabilities *schemav2.AuthCapabilities) (*schemav1.AuthCapabilities, error) {
	terminal := capabilities != nil && capabilities.Terminal != nil
	if terminal && capabilities.Terminal.Meta != nil {
		return nil, errors.New("v2 AuthCapabilities.terminal metadata cannot be represented in v1")
	}
	result := &schemav1.AuthCapabilities{Terminal: &terminal}
	if capabilities != nil {
		result.Meta = capabilities.Meta
	}
	return result, nil
}

// convert moves a value between the structurally identical v1 and v2 types.
func convert[T any](src any) (T, error) {
	var out T
	data, err := json.Marshal(src)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(data, &out)
	return out, err
}

// rejectAndClose answers the first request with an error and hangs up.
func rejectAndClose(ctx context.Context, transport acp.Transport, id jsontext.Value, reqErr *acp.RequestError) error {
	response := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    reqErr.Code,
			"message": reqErr.Message,
		},
	}
	if reqErr.Data != nil {
		response["error"].(map[string]any)["data"] = reqErr.Data
	}
	data, err := json.Marshal(response)
	if err != nil {
		return err
	}
	writeErr := transport.WriteMessage(ctx, data)
	closeErr := transport.Close()
	return errors.Join(writeErr, closeErr)
}

// replayTransport hands back the rewritten initialize message first and then
// delegates to the real transport.
type replayTransport struct {
	acp.Transport

	mu    sync.Mutex
	first jsontext.Value
}

func (t *replayTransport) ReadMessage(ctx context.Context) (jsontext.Value, error) {
	t.mu.Lock()
	first := t.first
	t.first = nil
	t.mu.Unlock()
	if first != nil {
		return first, nil
	}
	return t.Transport.ReadMessage(ctx)
}
