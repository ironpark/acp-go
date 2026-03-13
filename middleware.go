package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"
)

// LoggingMiddleware logs method calls, their duration, and any errors.
//
// Uses the provided logger function for output. If logger is nil, log.Printf is used.
func LoggingMiddleware(logger func(format string, args ...any)) Middleware {
	if logger == nil {
		logger = log.Printf
	}
	return func(next MethodHandler) MethodHandler {
		return func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			start := time.Now()
			result, err := next(ctx, method, params)
			elapsed := time.Since(start)
			if err != nil {
				logger("[ACP] %s failed (%s): %v", method, elapsed, err)
			} else {
				logger("[ACP] %s completed (%s)", method, elapsed)
			}
			return result, err
		}
	}
}

// RecoveryMiddleware catches panics in handlers and converts them to InternalError responses.
//
// This prevents a panicking handler from crashing the entire connection.
func RecoveryMiddleware() Middleware {
	return func(next MethodHandler) MethodHandler {
		return func(ctx context.Context, method string, params json.RawMessage) (result any, err error) {
			defer func() {
				if r := recover(); r != nil {
					err = ErrInternalError(nil, fmt.Sprintf("panic in handler for %s: %v", method, r))
				}
			}()
			return next(ctx, method, params)
		}
	}
}

// TimeoutMiddleware applies a timeout to all handler invocations.
//
// If a handler does not complete within the specified duration,
// the context is cancelled and the handler should return promptly.
func TimeoutMiddleware(timeout time.Duration) Middleware {
	return func(next MethodHandler) MethodHandler {
		return func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			return next(ctx, method, params)
		}
	}
}

// MethodFilterMiddleware only applies the inner middleware to methods matching the filter.
//
// This is useful for applying middleware to specific methods only, such as
// adding authentication checks to certain endpoints.
func MethodFilterMiddleware(filter func(method string) bool, mw Middleware) Middleware {
	return func(next MethodHandler) MethodHandler {
		filtered := mw(next)
		return func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			if filter(method) {
				return filtered(ctx, method, params)
			}
			return next(ctx, method, params)
		}
	}
}
