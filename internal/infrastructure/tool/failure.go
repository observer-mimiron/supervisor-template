package tool

import (
	"context"
	"errors"

	"github.com/observer-mimiron/supervisor-template/internal/application"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/mcp"
)

// FailureKind preserves adapter details while mapping them to the shared application error classes.
type FailureKind string

const (
	FailureTimeout       FailureKind = "timeout"
	FailureUnavailable   FailureKind = "unavailable"
	FailureProtocol      FailureKind = "protocol"
	FailureBusiness      FailureKind = "business"
	FailureInvalidOutput FailureKind = "invalid_output"
	FailureCanceled      FailureKind = "canceled"
	FailurePolicyDenied  FailureKind = "policy_denied"
)

// InvocationFailure records the stable failure kind without exposing adapter details publicly.
type InvocationFailure struct {
	Kind FailureKind
	Err  error
}

func (e *InvocationFailure) Error() string {
	if e == nil || e.Err == nil {
		return string(FailureBusiness)
	}
	return e.Err.Error()
}

func (e *InvocationFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// ApplicationClass lets application ErrorPolicy consume the normalized failure structurally.
func (e *InvocationFailure) ApplicationClass() application.ErrorClass {
	if e == nil {
		return application.ErrorInternal
	}
	switch e.Kind {
	case FailureTimeout:
		return application.ErrorTimeout
	case FailureUnavailable:
		return application.ErrorUnavailable
	case FailureInvalidOutput, FailureProtocol:
		return application.ErrorInvalidOutput
	case FailureCanceled:
		return application.ErrorCanceled
	case FailurePolicyDenied:
		return application.ErrorPolicyDenied
	default:
		return application.ErrorBusiness
	}
}

func normalizeInvocationError(err error) error {
	if err == nil {
		return nil
	}
	var normalized *InvocationFailure
	if errors.As(err, &normalized) {
		return err
	}
	var preCall *application.PreCallError
	if errors.As(err, &preCall) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &InvocationFailure{Kind: FailureTimeout, Err: err}
	}
	if errors.Is(err, context.Canceled) {
		return &InvocationFailure{Kind: FailureCanceled, Err: err}
	}
	var remote *mcp.Error
	if errors.As(err, &remote) {
		kind := FailureBusiness
		switch remote.Class {
		case mcp.ErrorTimeout:
			kind = FailureTimeout
		case mcp.ErrorUnavailable, mcp.ErrorService:
			kind = FailureUnavailable
		case mcp.ErrorProtocol:
			kind = FailureProtocol
		case mcp.ErrorDenied:
			kind = FailurePolicyDenied
		case mcp.ErrorBusiness, mcp.ErrorHTTP, mcp.ErrorInvalidConfig:
			kind = FailureBusiness
		}
		return &InvocationFailure{Kind: kind, Err: err}
	}
	return &InvocationFailure{Kind: FailureBusiness, Err: err}
}
