package acpmcp

import (
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	acp "github.com/ironpark/acp-go"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

// protocolVersion is the only MCP revision the binding carries.
const protocolVersion = "2026-07-28"

// Binding errors: outer ACP errors for an mcp/message request the binding
// itself could not admit, route or complete. An MCP error from the server is
// not one of them; it travels inside the response.
const (
	// ErrorCodeResourceLimit reports a binding resource limit exceeded.
	ErrorCodeResourceLimit acp.ErrorCode = -33000
	// ErrorCodeServerUnavailable reports a serverId that names no registered server.
	ErrorCodeServerUnavailable acp.ErrorCode = -33001
	// ErrorCodeBackendFailed reports a server that ended without an MCP outcome.
	ErrorCodeBackendFailed acp.ErrorCode = -33002
)

// errNoOutcome is returned for a response carrying neither outcome.
var errNoOutcome = &acp.RequestError{Code: ErrorCodeBackendFailed, Message: "mcp/message response has no outcome"}

// outcome is the inner MCP outcome of one mcp/message request: the result,
// or the MCP error when err is set.
type outcome struct {
	result jsontext.Value
	err    *jsonrpc.Error
}

// message is one MCP message carried by mcp/message, in either direction.
type message struct {
	serverID, requestID, method string
	params                      map[string]jsontext.Value
}

// toParams and fromParams convert between MCP's raw params and the object
// mcp/message flattens them into. MCP params are always objects.
func toParams(raw json.RawMessage) (map[string]jsontext.Value, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var params map[string]jsontext.Value
	if err := jsonv2.Unmarshal(raw, &params); err != nil {
		return nil, fmt.Errorf("acpmcp: MCP params must be an object: %w", err)
	}
	return params, nil
}

func fromParams(params map[string]jsontext.Value) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	return jsonv2.Marshal(params)
}

// toWireError turns the outer error of an mcp/message call into the MCP
// error the local MCP client sees.
func toWireError(err error) *jsonrpc.Error {
	if re, ok := errors.AsType[*acp.RequestError](err); ok {
		wire := &jsonrpc.Error{Code: int64(re.Code), Message: re.Message}
		if re.Data != nil {
			wire.Data, _ = jsonv2.Marshal(re.Data)
		}
		return wire
	}
	return &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: err.Error()}
}
