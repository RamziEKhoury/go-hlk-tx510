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

// WithBaud overrides the default baud rate (115200) used to open the serial
// port. The TX510's wire-supported rates are 9600, 19200, 38400, 57600, and
// 115200.
func WithBaud(baud int) Option {
	return func(c *config) { c.baud = baud }
}

// New opens portName as an 8N1 serial port and returns a Client ready to talk
// to a TX510 module. By default the port is opened at 115200 baud (the
// factory default); pass WithBaud to override.
//
// Close the Client when done:
//
//	client, err := tx510.New("/dev/ttyUSB0")
//	if err != nil {
//	    return err
//	}
//	defer client.Close()
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

// serialTransport adapts go.bug.st/serial.Port to Transport. It translates
// the absolute SetReadDeadline used by Client into the relative SetReadTimeout
// that the underlying library exposes.
type serialTransport struct {
	port serial.Port
}

func (s *serialTransport) Read(p []byte) (int, error)  { return s.port.Read(p) }
func (s *serialTransport) Write(p []byte) (int, error) { return s.port.Write(p) }

// SetReadDeadline translates an absolute deadline into the relative timeout
// the underlying serial library uses. A zero time disables the timeout; a
// deadline already in the past sets the timeout to zero so the next Read
// returns immediately (this is how Client implements context cancellation).
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

// Close releases the underlying serial port.
func (s *serialTransport) Close() error { return s.port.Close() }

// Reconfigure changes the host-side baud rate (8N1) without reopening the
// port. Client.SetBaudRate calls this automatically after a successful
// device-side baud change.
func (s *serialTransport) Reconfigure(baud int) error {
	return s.port.SetMode(&serial.Mode{
		BaudRate: baud,
		DataBits: 8,
		Parity:   serial.NoParity,
		StopBits: serial.OneStopBit,
	})
}
