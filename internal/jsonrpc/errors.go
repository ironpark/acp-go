package jsonrpc

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
)

// ErrorCode is a JSON-RPC 2.0 error code.
//
// The values mirror the ACP `ErrorCode` schema, but this package stays
// independent of any protocol version.
type ErrorCode int64

const (
	CodeParseError       ErrorCode = -32700
	CodeInvalidRequest   ErrorCode = -32600
	CodeMethodNotFound   ErrorCode = -32601
	CodeInvalidParams    ErrorCode = -32602
	CodeInternalError    ErrorCode = -32603
	CodeRequestCancelled ErrorCode = -32800
	CodeAuthRequired     ErrorCode = -32000
	CodeResourceNotFound ErrorCode = -32002
)

// RequestError is a JSON-RPC error object carried as a Go error.
//
// Handlers return it to control the code, message and data of the error
// response; peer errors are returned to callers in the same shape.
type RequestError struct {
	Code    ErrorCode
	Message string
	// Data is marshaled into the error object's "data" member. It is nil when
	// the peer sent no data.
	Data any
}

func (e *RequestError) Error() string {
	if e.Data != nil {
		return fmt.Sprintf("jsonrpc error %d: %s (data: %v)", e.Code, e.Message, e.Data)
	}
	return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message)
}

// Is reports whether e matches target, a *RequestError, by code and, when
// target has one, by message; data is not compared. So an error the peer sent
// matches the sentinel it was built from, and a target with only a code
// matches every error with that code:
//
//	errors.Is(err, acp.ErrTurnInProgress)
//	errors.Is(err, &acp.RequestError{Code: acp.ErrorCodeAuthRequired})
func (e *RequestError) Is(target error) bool {
	t, ok := target.(*RequestError)
	return ok && t != nil && e.Code == t.Code && (t.Message == "" || t.Message == e.Message)
}

// WithData returns a copy of e whose "data" member is data:
//
//	return nil, acp.ResourceNotFound(path).WithData(map[string]string{"uri": path})
func (e *RequestError) WithData(data any) *RequestError {
	c := *e
	c.Data = data
	return &c
}

// message appends ": detail" to a base message, mirroring the reference SDKs.
func message(base, detail string) string {
	if detail != "" {
		return base + ": " + detail
	}
	return base
}

// ParseError reports invalid JSON received by the peer (-32700). A non-empty
// detail follows the standard message.
func ParseError(detail string) *RequestError {
	return &RequestError{Code: CodeParseError, Message: message("Parse error", detail)}
}

// InvalidRequest reports a malformed request object (-32600). A non-empty
// detail follows the standard message.
func InvalidRequest(detail string) *RequestError {
	return &RequestError{Code: CodeInvalidRequest, Message: message("Invalid request", detail)}
}

// MethodNotFound reports an unknown or unsupported method (-32601).
func MethodNotFound(method string) *RequestError {
	return &RequestError{
		Code:    CodeMethodNotFound,
		Message: fmt.Sprintf("Method not found: %s", method),
		Data:    map[string]string{"method": method},
	}
}

// InvalidParams reports parameters that failed validation (-32602). A
// non-empty detail follows the standard message.
func InvalidParams(detail string) *RequestError {
	return &RequestError{Code: CodeInvalidParams, Message: message("Invalid params", detail)}
}

// InternalError reports a handler failure (-32603). A non-empty detail
// follows the standard message.
func InternalError(detail string) *RequestError {
	return &RequestError{Code: CodeInternalError, Message: message("Internal error", detail)}
}

// RequestCancelled reports that a request was cancelled before completing
// (-32800). A non-empty detail follows the standard message.
//
// See the [request cancellation RFD].
//
// [request cancellation RFD]: https://agentclientprotocol.com/protocol/rfds/request-cancellation
func RequestCancelled(detail string) *RequestError {
	return &RequestError{Code: CodeRequestCancelled, Message: message("Request cancelled", detail)}
}

// AuthRequired reports that the caller must authenticate first (-32000). A
// non-empty detail follows the standard message.
func AuthRequired(detail string) *RequestError {
	return &RequestError{Code: CodeAuthRequired, Message: message("Authentication required", detail)}
}

// ResourceNotFound reports a missing resource such as a file (-32002). A
// non-empty detail follows the standard message; attach the resource's URI
// with [RequestError.WithData] when the peer should be able to read it.
func ResourceNotFound(detail string) *RequestError {
	return &RequestError{Code: CodeResourceNotFound, Message: message("Resource not found", detail)}
}

// wireError is the JSON-RPC error object as it appears on the wire.
type wireError struct {
	Code    ErrorCode      `json:"code"`
	Message string         `json:"message"`
	Data    jsontext.Value `json:"data,omitzero"`
}

// ErrorObject returns err as the members of a JSON-RPC error object, as an
// error response would carry it: a *RequestError keeps its own, and any
// other error is an internal error with its message.
func ErrorObject(err error) (code ErrorCode, message string, data jsontext.Value) {
	re, ok := errors.AsType[*RequestError](err)
	if !ok {
		re = InternalError(err.Error())
	}
	w := re.toWire()
	return w.Code, w.Message, w.Data
}

// FromErrorObject returns the *RequestError a JSON-RPC error object
// describes, as a received error response would.
func FromErrorObject(code ErrorCode, message string, data jsontext.Value) *RequestError {
	return (&wireError{Code: code, Message: message, Data: data}).toRequestError()
}

func (e *wireError) toRequestError() *RequestError {
	err := &RequestError{Code: e.Code, Message: e.Message}
	if len(e.Data) > 0 {
		err.Data = e.Data.Clone()
	}
	return err
}

func (e *RequestError) toWire() *wireError {
	out := &wireError{Code: e.Code, Message: e.Message}
	if e.Data == nil {
		return out
	}
	data, err := json.Marshal(e.Data)
	if err != nil {
		// Keep the error response well-formed even if the payload is not.
		data, _ = json.Marshal(fmt.Sprint(e.Data))
	}
	out.Data = data
	return out
}
