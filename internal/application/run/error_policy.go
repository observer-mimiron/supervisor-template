package run

import (
	"context"
	"errors"

	"github.com/observer-mimiron/supervisor-template/internal/application"
)

// DefaultErrorPolicy 保持错误分类和重试边界集中在应用层。
type DefaultErrorPolicy struct{}

func (DefaultErrorPolicy) Classify(err error, phase application.ExecutionPhase) application.ErrorClass {
	if err == nil {
		return ""
	}
	if errors.Is(err, application.ErrOutcomeUnknown) || phase == application.PhaseCommit {
		return application.ErrorUnknownOutcome
	}
	if errors.Is(err, context.Canceled) {
		return application.ErrorCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return application.ErrorTimeout
	}
	var classified interface{ ApplicationClass() application.ErrorClass }
	if errors.As(err, &classified) {
		return classified.ApplicationClass()
	}
	var preCall *application.PreCallError
	if errors.As(err, &preCall) && phase == application.PhasePreCall {
		return application.ErrorPreCallFailure
	}
	return application.ErrorBusiness
}

func (DefaultErrorPolicy) Decide(class application.ErrorClass, attempt, retryLimit int) application.RetryDecision {
	if class == application.ErrorUnknownOutcome {
		return application.Reconcile
	}
	if class == application.ErrorPreCallFailure && attempt <= retryLimit {
		return application.Retry
	}
	return application.Terminate
}
