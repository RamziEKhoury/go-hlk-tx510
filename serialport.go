package tx510

import (
	"fmt"
	"time"

	"go.bug.st/serial"
)

// Option configures a Client constructed by New.
type Option func(*config)

type config struct {
	baud int
}

// WithBaud overrides the default 115200 baud used to open the port.
func WithBaud(baud int) Option {
	return func(c *config) { c.baud = baud }
}

// New opens portName as an 8N1 serial port (default 115200) and returns a Client.
func New(portName string, opts ...Option) (*Client, error) {
	cfg := &config{baud: 115200}
	for _, o := range opts {
		o(cfg)
	}
	port, err := serial.Open(portName, &serial.Mode{
		BaudRate: cfg.baud,
		DataBits: 8,
		Parity:   serial.NoParity,
		StopBits: serial.OneStopBit,
	})
	if err != nil {
		return nil, fmt.Errorf("tx510: open %s: %w", portName, err)
	}
	t := &serialTransport{port: port}
	return &Client{t: t, closer: t}, nil
}

type serialTransport struct {
	port serial.Port
}

func (s *serialTransport) Read(p []byte) (int, error)  { return s.port.Read(p) }
func (s *serialTransport) Write(p []byte) (int, error) { return s.port.Write(p) }

// SetReadDeadline translates an absolute deadline into the relative timeout the underlying serial library uses.
func (s *serialTransport) SetReadDeadline(t time.Time) error {
	if t.IsZero() {
		return s.port.SetReadTimeout(serial.NoTimeout)
	}
	d := time.Until(t)
	if d < 0 {
		d = 0
	}
	return s.port.SetReadTimeout(d)
}

func (s *serialTransport) Close() error { return s.port.Close() }

func (s *serialTransport) Flush() error { return s.port.ResetInputBuffer() }

// Reconfigure changes the host-side baud rate (8N1) without reopening the port.
func (s *serialTransport) Reconfigure(baud int) error {
	return s.port.SetMode(&serial.Mode{
		BaudRate: baud,
		DataBits: 8,
		Parity:   serial.NoParity,
		StopBits: serial.OneStopBit,
	})
}
