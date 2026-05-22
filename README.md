# go-hlk-tx510

A Go driver for the **Hi-Link HLK-TX510** face-recognition module, speaking the device's native serial framing protocol.

> **Note on authorship.** This documentation was drafted with the assistance of an AI coding assistant (Anthropic's Claude). It has been reviewed by the maintainers, but please open an issue if you spot anything inaccurate or unclear.

---

## Contents

- [Overview](#overview)
- [Installation](#installation)
- [Quick start](#quick-start)
- [The `Transport` interface](#the-transport-interface)
- [API reference](#api-reference)
  - [Enrollment & recognition](#enrollment--recognition)
  - [Templates (eigenvalues)](#templates-eigenvalues)
  - [User management](#user-management)
  - [Device info & control](#device-info--control)
- [Contexts, timeouts, and cancellation](#contexts-timeouts-and-cancellation)
- [Concurrency](#concurrency)
- [Error handling](#error-handling)
- [Wire format](#wire-format)
- [Caveats](#caveats)
- [Versioning](#versioning)
- [License](#license)

---

## Overview

The HLK-TX510 is a small face-recognition module that exposes a binary command/response protocol over a UART link (TTL serial, default 115200 8N1). This package implements that protocol so you can drive the module from Go without hand-rolling frames, parity bytes, or chunked template transfers.

Capabilities exposed by the `Client`:

- Register and recognize faces, retrieving the assigned `faceID`.
- Read and write 1024-byte face templates ("eigenvalues") in the device-defined chunked format.
- List enrolled users and delete one or all of them.
- Query firmware version, toggle backlight / display / white-light, change baud rate, and reboot.

The transport layer is abstracted behind a one-method-extension of `io.ReadWriter`, so the same `Client` works against a real serial port, an in-memory fake, or a network bridge — see [The `Transport` interface](#the-transport-interface).

## Installation

```sh
go get github.com/RamziEKhoury/go-hlk-tx510
```

Requires Go 1.21+ (uses `binary.BigEndian.AppendUint32` and standard `context` cancellation patterns).

## Quick start

```go
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	tx510 "github.com/RamziEKhoury/go-hlk-tx510"
)

func main() {
	client, err := tx510.New("/dev/ttyUSB0")
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	version, err := client.Version(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("firmware:", version)

	// Enrollment: hold a face in front of the module. Allow a generous deadline.
	enrollCtx, cancelEnroll := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelEnroll()

	faceID, err := client.Register(enrollCtx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("enrolled faceID:", faceID)
}
```

`tx510.New(port)` opens the port as 8N1 at 115200 baud — the TX510's factory defaults. Override the baud rate with `tx510.WithBaud(...)`:

```go
client, err := tx510.New("/dev/ttyUSB0", tx510.WithBaud(57600))
```

For tests, network bridges, or non-`go.bug.st/serial` backends, implement [`Transport`](#the-transport-interface) and use `tx510.NewWithTransport(t)` instead.

## The `Transport` interface

```go
type Transport interface {
	io.ReadWriter
	SetReadDeadline(t time.Time) error
}
```

`Client` only depends on this interface, so any implementation is acceptable. Two notes:

- `SetReadDeadline` is the cancellation hook. `Client.do` sets a read deadline derived from the caller's `context.Context` and, on `ctx.Done()`, forces `SetReadDeadline(time.Now())` to abort a stuck read. Implementations that only support a *timeout* (e.g. `go.bug.st/serial`) should translate `time.Until(deadline)` as shown above.
- Writes are not deadlined. If your transport can block indefinitely on `Write`, wrap it accordingly.

## API reference

Constructors:

```go
// Opens a real serial port. Most callers use this.
func New(portName string, opts ...Option) (*Client, error)

// Plug in any Transport — useful for tests or non-serial backends.
func NewWithTransport(t Transport) *Client
```

Lifecycle:

```go
func (c *Client) Close() error  // closes the serial port (no-op for NewWithTransport)
```

`Client` is safe for concurrent use; all methods serialize through an internal mutex so the half-duplex device never sees overlapping commands.

### Enrollment & recognition

```go
func (c *Client) Register(ctx context.Context)  (faceID uint16, err error)
func (c *Client) Recognize(ctx context.Context) (faceID uint16, err error)
```

- `Register` enrolls the face currently visible to the module and returns its assigned `faceID`.
- `Recognize` matches the face currently visible against the on-device gallery and returns the matched `faceID`.

Both block on the device side until a face is presented (or it gives up), so use a generous `ctx` deadline — typically 10–30 seconds.

Failure modes are exposed as a `*ResultError` wrapping a `ResultCode` — see [Error handling](#error-handling). Common ones for these calls:

- `ResultNoFace` — no face was visible within the device's window.
- `ResultPoseAngleTooLarge` — face too far / pose too oblique.
- `ResultLiveness2DFailed`, `ResultLiveness3DFailed` — anti-spoofing rejected the input.
- `ResultMatchFailed` — `Recognize` only: no enrolled user matched.
- `ResultDuplicateFace` — `Register` only: this face is already enrolled.

### Templates (eigenvalues)

```go
const TemplateSize = 1024

func (c *Client) ReadTemplate(ctx context.Context, faceID uint16) ([]byte, error)
func (c *Client) WriteTemplate(ctx context.Context, template []byte) error
```

`ReadTemplate` extracts the 1024-byte template ("eigenvalue") for a previously enrolled `faceID`. Useful for backing up enrollments off-device or transferring them between modules.

`WriteTemplate` uploads a 1024-byte template back to the module. The protocol requires the upload be split into four 256-byte chunks with a strict sequence-number handshake; the client handles this transparently. The template must be exactly `TemplateSize` bytes — passing any other length returns an error without touching the wire.

### User management

```go
func (c *Client) UserCount(ctx context.Context) (faceIDs []uint16, err error)
func (c *Client) DeleteUser(ctx context.Context, faceID uint16) error
func (c *Client) DeleteAll(ctx context.Context) error
```

`UserCount` returns the slice of currently enrolled `faceID`s. The slice length is the user count; the values are the IDs themselves. Returns `nil, nil` if the device reports an empty gallery.

`DeleteUser` removes one enrollment. `DeleteAll` wipes the gallery — there is no confirmation step on the device, so guard this in your application.

### Device info & control

```go
func (c *Client) Version(ctx context.Context) (string, error)

func (c *Client) Reboot(ctx context.Context) error

func (c *Client) SetBacklight(ctx context.Context, on bool) error
func (c *Client) SetDisplay(ctx context.Context, on bool) error
func (c *Client) SetWhiteLight(ctx context.Context, on bool) error

func (c *Client) SetBaudRate(ctx context.Context, rate BaudRate) error
```

`BaudRate` values:

| Constant      | Wire value | Bits/s   |
| ------------- | ---------- | -------- |
| `Baud9600`    | `0x00`     | 9 600    |
| `Baud19200`   | `0x01`     | 19 200   |
| `Baud38400`   | `0x02`     | 38 400   |
| `Baud57600`   | `0x03`     | 57 600   |
| `Baud115200`  | `0x04`     | 115 200 (factory default) |

See [Caveats](#caveats) for important notes on `SetBaudRate` and `Reboot`.

## Contexts, timeouts, and cancellation

Every API method takes a `context.Context`. The client uses it in two ways:

1. **Deadline → read timeout.** Before each request, the caller's deadline is pushed into `Transport.SetReadDeadline`. If the context has no deadline, a 5-second default applies. Long-running operations (`Register`, `Recognize`) almost always want an explicit longer deadline.
2. **Cancellation → read abort.** If `ctx` is cancelled while the client is waiting for a reply, a watcher goroutine forces a read deadline of `time.Now()`, unblocking the read. The call returns `ctx.Err()` rather than the read error in that case.

Cancelling does **not** abort an in-flight `Write`. If you suspect a wedged transport, you must close the underlying port yourself.

## Concurrency

`Client` serializes commands with an internal mutex. You can share one `*Client` across goroutines safely; commands will execute one at a time in arrival order. There is no command pipelining — the device protocol is strictly request/response and the client honors that.

## Error handling

All command methods return one of:

- `nil` — the device returned `ResultSuccess` and any payload was parsed cleanly.
- A wrapped I/O error (`tx510: write …`, `tx510: read … reply: …`) — the wire conversation failed.
- A framing error (`ErrBadHeader`, `ErrParityMismatch`, `ErrFrameTooShort`, `ErrFrameTooLarge`) — the reply was malformed.
- A `*ResultError` — the device responded correctly but reported a non-success `ResultCode`.

Recognising a `ResultError` is the standard way to branch on device-side outcomes:

```go
faceID, err := client.Recognize(ctx)
switch {
case err == nil:
	handleMatch(faceID)
case errors.Is(err, context.DeadlineExceeded):
	// caller-side timeout
case func() bool {
	var rerr *tx510.ResultError
	return errors.As(err, &rerr) && rerr.Code == tx510.ResultNoFace
}():
	// no face presented — retry on next trigger
default:
	log.Printf("recognize: %v", err)
}
```

`ResultCode` implements `fmt.Stringer` and `IsSuccess()`. Unknown codes stringify as `unknown(0xNN)` rather than panicking.

## Wire format

For reference (and to help anyone implementing a fake `Transport`), every frame on the wire looks like:

```
+--------+--------+--------+----------------+-------------------+--------+
| 0xEF   | 0xAA   | msgID  |   size (BE32)  |       data        | parity |
| 1 byte | 1 byte | 1 byte |    4 bytes     |    `size` bytes   | 1 byte |
+--------+--------+--------+----------------+-------------------+--------+
```

- `msgID` is the command ID for requests, and the *acked* command ID for replies.
- `size` is the length of `data` only — it excludes the header and the parity byte.
- `parity` is the 8-bit sum (mod 256) of every byte from `msgID` through the last `data` byte. The two magic bytes are excluded from the parity.
- Reply `data` is always at least two bytes: `ackedID(1) | result(1)`, optionally followed by a command-specific payload.

`MaxFrameSize` (4 KiB) caps how many bytes the parser will accept for a single frame, protecting against garbage on the wire claiming an enormous size.

## Caveats

A few sharp edges to keep in mind in production:

- **`SetBaudRate` is host-aware when you used `New`.** The call issues the device-side command and, on success, reconfigures the host port to match. If you used `NewWithTransport` with your own backend, only the device side changes — you must reconfigure the host yourself or implement `Reconfigure(baud int) error` on your Transport so `SetBaudRate` can pick it up via interface assertion.
- **`Reboot` may not ACK.** Depending on firmware revision the module may reset before it sends its acknowledgement, in which case `Reboot` returns a read-timeout error even though the reboot succeeded. Treat a read timeout from `Reboot` as "probably fine" and wait a second or two before issuing the next command.
- **Recognition/registration timeouts.** The on-device matcher can take several seconds to give up when no usable face is presented. The 5-second default deadline is fine for control-plane commands but too short for `Register` / `Recognize`. Pass an explicit longer `ctx`.
- **`DeleteAll` is irrevocable.** There is no confirmation step on the device. Gate it behind your own UI confirmation.
- **Writes are not deadlined.** A wedged port can stall a `Write`. If that's a concern, close the underlying handle externally to release the call.

## Versioning

This package follows [Semantic Versioning](https://semver.org/). Pre-1.0 releases may make breaking changes between minor versions; once tagged `v1.0.0`, the exported surface above will be considered stable.

## License

See `LICENSE` in the repository root.
