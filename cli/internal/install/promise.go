// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package install

import (
	"context"
)

type Promise[T any] struct {
	function func(context.Context) (T, error)
	channel  chan T
	result   T
	err      error
}

type UntypedPromise interface {
	AwaitErr() error
}

type PromiseGroup []UntypedPromise

func NewPromise[T any](ctx context.Context, group *PromiseGroup, function func(context.Context) (T, error)) *Promise[T] {
	p := &Promise[T]{
		function: function,
		channel:  make(chan T),
	}

	go func() {
		p.result, p.err = p.function(ctx)
		close(p.channel)
	}()

	*group = PromiseGroup(append(*group, p))
	return p
}

func NewPromiseAfter[T any](ctx context.Context, group *PromiseGroup, function func(context.Context) (T, error), dependencies ...UntypedPromise) *Promise[T] {
	f := func(ctx context.Context) (T, error) {
		for _, d := range dependencies {
			if err := d.AwaitErr(); err != nil {
				var defaultT T
				return defaultT, errDependencyFailed
			}
		}

		return function(ctx)
	}

	return NewPromise(ctx, group, f)
}

func (p *Promise[T]) Await() (T, error) {
	<-p.channel
	return p.result, p.err
}

func (p *Promise[T]) AwaitErr() error {
	<-p.channel
	return p.err
}
