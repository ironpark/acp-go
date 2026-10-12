package acp1

import (
	"github.com/ironpark/acp-go/internal/jsonrpc"
	schema "github.com/ironpark/acp-go/schema/v1"
)

// subagentIdle is the idle state a subagent reports for reason, or none for
// "". The "error" stop reason, which v1 has only for subagents since a v1
// prompt reports failure in its error response, is built by IdleError.
func subagentIdle(reason StopReason) IdleStateUpdate {
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
	}
	return schema.NewIdleStateUpdate(schema.IdleStateUpdateCustom{StopReason: string(reason)})
}

// errorObject is failure as the JSON-RPC error object an error response
// would carry.
func errorObject(failure error) *schema.Error {
	code, message, data := jsonrpc.ErrorObject(failure)
	return &schema.Error{Code: schema.ErrorCode(code), Message: message, Data: data}
}
