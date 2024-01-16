// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package logging

import (
	"context"
	"io"
)

type logSinkContextKeyType int

const logSinkKey logSinkContextKeyType = iota

func GetLogSinkFromContext(ctx context.Context) io.Writer {
	return ctx.Value(logSinkKey).(io.Writer)
}

func SetLogSinkOnContext(ctx context.Context, logSink io.Writer) context.Context {
	return context.WithValue(ctx, logSinkKey, logSink)
}
