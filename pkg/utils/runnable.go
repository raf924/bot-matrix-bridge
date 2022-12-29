package utils

import (
	"context"
	"time"
)

var _ context.Context = (*RunningContext)(nil)

type RunningContext struct {
	ctx       context.Context
	cancel    func(err error)
	doneFuncs []func()
	startFn   func(ctx context.Context) error
}

func Runnable(parentCtx context.Context, f func(ctx context.Context) error) *RunningContext {
	r := &RunningContext{}
	r.ctx, r.cancel = Errorable(parentCtx)
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
	if err == nil && r.Err() != nil {
		return r.Err()
	}
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
	return r.ctx.Err()
}

func (r *RunningContext) Critical(f func(ctx context.Context) error) error {
	ctx, cancel := context.WithCancel(r.ctx)
	err := f(ctx)
	if err != nil {
		r.cancel(err)
	}
	cancel()
	return err
}

func (r *RunningContext) OnDone(f func()) {
	if r.ctx.Err() != nil {
	}
	r.doneFuncs = append(r.doneFuncs, f)
}
