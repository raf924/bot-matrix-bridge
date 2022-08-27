package utils

import (
	"context"
	"sync/atomic"
	"time"
)

var _ context.Context = (*RunningContext)(nil)

type RunningContext struct {
	ctx       context.Context
	cancel    context.CancelFunc
	err       *atomic.Value
	doneFuncs []func()
	startFn   func(ctx context.Context) error
}

func Runnable(parentCtx context.Context, f func(ctx context.Context) error) *RunningContext {
	r := &RunningContext{err: &atomic.Value{}}
	r.ctx, r.cancel = context.WithCancel(parentCtx)
	r.startFn = f
	return r
}

func (r *RunningContext) Run() error {
	go func() {
		ctx, cancel := context.WithCancel(r.ctx)
		<-ctx.Done()
		for _, doneFunc := range r.doneFuncs {
			doneFunc()
		}
		cancel()
	}()
	ctx, cancel := context.WithCancel(r.ctx)
	err := r.startFn(ctx)
	cancel()
	return err
}

func (r *RunningContext) Deadline() (deadline time.Time, ok bool) {
	return
}

func (r *RunningContext) Value(_ any) any {
	return nil
}

func (r *RunningContext) Done() <-chan struct{} {
	return r.ctx.Done()
}

func (r *RunningContext) Err() error {
	return r.err.Load().(error)
}

func (r *RunningContext) Critical(f func(ctx context.Context) error) error {
	ctx, cancel := context.WithCancel(r.ctx)
	err := f(ctx)
	if err != nil {
		r.err.Store(err)
		r.cancel()
	}
	cancel()
	return err
}

func (r *RunningContext) OnDone(f func()) {
	if r.err.Load() != nil {
	}
	r.doneFuncs = append(r.doneFuncs, f)
}
