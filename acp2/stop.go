package acp2

import (
	acp "github.com/ironpark/acp-go"
	"github.com/ironpark/acp-go/internal/jsonrpc"
	schema "github.com/ironpark/acp-go/schema/v2"
)

// StopReason is why foreground work ended, as an idle state update reports
// it. Values beginning with an underscore are custom; "" means the agent gave
// none.
type StopReason string

const (
	StopReasonEndTurn         StopReason = "end_turn"
	StopReasonMaxTokens       StopReason = "max_tokens"
	StopReasonMaxTurnRequests StopReason = "max_turn_requests"
	StopReasonRefusal         StopReason = "refusal"
	StopReasonCancelled       StopReason = "cancelled"
	// StopReasonError ends work that failed. The failure travels with it as
	// a JSON-RPC error object; see [SessionStream.IdleError].
	StopReasonError StopReason = "error"
)

// idleState is the idle state update for reason, or none for "". The error
// stop reason carries no failure here; see errorState.
func idleState(reason StopReason) IdleStateUpdate {
	switch reason {
	case "":
		return schema.NewIdleStateUpdate(schema.IdleStateUpdateUntagged{})
	case StopReasonEndTurn:
		return schema.NewIdleStateUpdate(schema.IdleStateUpdateEndTurn{})
	case StopReasonMaxTokens:
		return schema.NewIdleStateUpdate(schema.IdleStateUpdateMaxTokens{})
	case StopReasonMaxTurnRequests:
		return schema.NewIdleStateUpdate(schema.IdleStateUpdateMaxTurnRequests{})
	case StopReasonRefusal:
		return schema.NewIdleStateUpdate(schema.IdleStateUpdateRefusal{})
	case StopReasonCancelled:
		return schema.NewIdleStateUpdate(schema.IdleStateUpdateCancelled{})
	case StopReasonError:
		return schema.NewIdleStateUpdate(schema.IdleStateUpdateError{})
	}
	return schema.NewIdleStateUpdate(schema.IdleStateUpdateCustom{StopReason: string(reason)})
}

// errorState is the error stop reason carrying failure as the JSON-RPC error
// object an error response would.
func errorState(failure error) IdleStateUpdate {
	code, message, data := jsonrpc.ErrorObject(failure)
	return schema.NewIdleStateUpdate(schema.IdleStateUpdateError{Error: &schema.Error{Code: schema.ErrorCode(code), Message: message, Data: data}})
}

// stopOf returns the stop reason an idle state update reports and, for the
// error stop reason, the failure as an [acp.RequestError].
func stopOf(idle IdleStateUpdate) (StopReason, error) {
	e, ok := idle.As[schema.IdleStateUpdateError]()
	if !ok {
		return StopReason(idle.Tag()), nil
	}
	if e.Error == nil {
		return StopReasonError, acp.InternalError("the agent reported a failure without details")
	}
	return StopReasonError, jsonrpc.FromErrorObject(jsonrpc.ErrorCode(e.Error.Code), e.Error.Message, e.Error.Data)
}
