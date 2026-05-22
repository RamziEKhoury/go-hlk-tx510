package tx510

import (
	"io"
	"time"
)


type Transport interface {
	io.ReadWriter
	SetReadDeadline(t time.Time) error
}
