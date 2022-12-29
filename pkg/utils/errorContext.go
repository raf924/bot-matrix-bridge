package utils

import (
	"context"
	"fmt"
)

type ErrorContext struct {
	context.Context
	parentCtx  context.Context
	cancelFunc context.CancelFunc
	err        error
}

func (e *ErrorContext) CancelWithError(err error) {
	e.err = err
	e.cancelFunc()
}

func (e *ErrorContext) Err() error {
	if e.parentCtx.Err() == nil {
		return e.err
	}
	return fmt.Errorf("parent context was cancelled: %w", e.parentCtx.Err())
}

var _ context.Context = (*ErrorContext)(nil)

func Errorable(parentContext context.Context) (context.Context, func(err error)) {
	ctx, cancelFunc := context.WithCancel(parentContext)
	ec := &ErrorContext{
		Context:    ctx,
		parentCtx:  parentContext,
		cancelFunc: cancelFunc,
		err:        nil,
	}
	return ec, ec.CancelWithError
}
