package utils

import (
	"context"
	"sync/atomic"
	"time"
)

var _ context.Context = (*RunningContext)(nil)

type RunningContext struct {
	ctx     context.Context
	cancel  context.CancelFunc
	err     *atomic.Value
	startFn func(ctx context.Context) error
}

func (r *RunningContext) Deadline() (deadline time.Time, ok bool) {
	return
}

func (r *RunningContext) Value(_ any) any {
	return nil
}

func Run(parentCtx context.Context, f func(ctx context.Context) error) (*RunningContext, error) {
	r := &RunningContext{err: &atomic.Value{}}
	r.ctx, r.cancel = context.WithCancel(parentCtx)
	return r, f(r.ctx)
}

func (r *RunningContext) Done() <-chan struct{} {
	return r.ctx.Done()
}

func (r *RunningContext) Err() error {
	return r.err.Load().(error)
}

func (r *RunningContext) Critical(f func(ctx context.Context) error) error {
	err := f(r.ctx)
	if err != nil {
		r.err.Store(err)
		r.cancel()
	}
	return err
}
