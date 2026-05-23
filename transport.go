package tx510

import (
	"io"
	"time"
)


type Transport interface {
	io.ReadWriter
	SetReadDeadline(t time.Time) error
}

// Flusher is an optional interface a Transport may implement to drop any
// bytes buffered in the OS receive queue before a new request is written.
type Flusher interface {
	Flush() error
}
