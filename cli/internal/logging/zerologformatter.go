package logging

import (
	"bytes"
	"io"
)

type ZerologFormatter struct {
	sink io.Writer
	buf  []byte
}

// Creates an io.Writer that writes lines of JSON to the given sink.
func NewZeroLogFormatter(sink io.Writer) *ZerologFormatter {
	return &ZerologFormatter{
		sink: sink,
	}
}

func (w *ZerologFormatter) Write(p []byte) (n int, err error) {
	w.buf = append(w.buf, p...)

	for {
		// Find the index of the next newline character.
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}

		line := w.buf[:i]
		w.sink.Write(line)
		w.buf = w.buf[i+1:]
	}

	return len(p), nil
}

func (w *ZerologFormatter) Flush() {
	if len(w.buf) > 0 {
		w.sink.Write(w.buf)
	}
}
