package setup

import "context"

type Promise[T any] struct {
	function func(context.Context) (T, error)
	channel  chan T
	result   T
	err      error
}

type UntypedPromise interface {
	AwaitErr() error
}

func NewPromise[T any](ctx context.Context, function func(context.Context) (T, error)) *Promise[T] {
	p := &Promise[T]{
		function: function,
		channel:  make(chan T),
	}

	go func() {
		p.result, p.err = p.function(ctx)
		close(p.channel)
	}()

	return p
}

func NewPromiseAfter[T any](ctx context.Context, function func(context.Context) (T, error), dependencies ...UntypedPromise) *Promise[T] {
	f := func(ctx context.Context) (T, error) {
		for _, d := range dependencies {
			if err := d.AwaitErr(); err != nil {
				var defaultT T
				return defaultT, err
			}
		}

		return function(ctx)
	}

	return NewPromise(ctx, f)
}

func (p *Promise[T]) Await() (T, error) {
	<-p.channel
	return p.result, p.err
}

func (p *Promise[T]) AwaitErr() error {
	<-p.channel
	return p.err
}
