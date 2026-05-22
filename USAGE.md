# Usage guide

Recipes for driving an HLK-TX510 from Go. For the full API reference (every method, the wire format, the `Transport` interface), see [`README.md`](README.md).

> **Note on authorship.** This guide was drafted with the assistance of an AI coding assistant (Anthropic's Claude). Please open an issue if you spot anything inaccurate.

---

## Contents

- [Before you start](#before-you-start)
  - [Find your serial port](#find-your-serial-port)
  - [Permissions](#permissions)
- [Recipes](#recipes)
  - [1. Open the device](#1-open-the-device)
  - [2. Enroll a face](#2-enroll-a-face)
  - [3. Recognize a face](#3-recognize-a-face)
  - [4. List enrolled users](#4-list-enrolled-users)
  - [5. Delete a user](#5-delete-a-user)
  - [6. Back up the gallery](#6-back-up-the-gallery)
  - [7. Restore a backup](#7-restore-a-backup)
  - [8. Change the baud rate](#8-change-the-baud-rate)
  - [9. Toggle backlight / display / LED](#9-toggle-backlight--display--led)
  - [10. Long-running service](#10-long-running-service)
- [Handling errors](#handling-errors)
- [Troubleshooting](#troubleshooting)

---

## Before you start

Install the module as a Go dependency:

```sh
go get github.com/RamziEKhoury/go-hlk-tx510
```

Wire the TX510's `RX`, `TX`, `GND`, and `VCC` pins to a USB-serial bridge (FTDI, CP2102, CH340, etc). The factory defaults are **115200 8N1**.

### Find your serial port

| OS      | How to find it                                       | Example                       |
| ------- | ---------------------------------------------------- | ----------------------------- |
| macOS   | `ls /dev/tty.usbserial-* /dev/cu.usbserial-*`        | `/dev/tty.usbserial-A50285BI` |
| Linux   | `dmesg \| tail -20` after plug-in, or `ls /dev/ttyUSB*` | `/dev/ttyUSB0`                |
| Windows | Device Manager → "Ports (COM & LPT)"                 | `COM3`                        |

### Permissions

On Linux, your user needs to be in the `dialout` group (or `uucp` on some distros) to open serial devices without root:

```sh
sudo usermod -a -G dialout $USER
# log out and back in
```

On macOS no special permissions are needed for USB-serial bridges. On Windows, the COM port is owned by the user that plugged in the device.

---

## Recipes

### 1. Open the device

```go
client, err := tx510.New("/dev/ttyUSB0")
if err != nil {
    return fmt.Errorf("open tx510: %w", err)
}
defer client.Close()
```

Non-default baud rate (only relevant if you previously called `SetBaudRate` and the device retained the setting across power cycles):

```go
client, err := tx510.New("/dev/ttyUSB0", tx510.WithBaud(57600))
```

`Close()` releases the serial port. Always defer it.

### 2. Enroll a face

`Register` blocks on the device until a face is presented or it gives up. Use a generous deadline.

```go
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()

faceID, err := client.Register(ctx)
if err != nil {
    return fmt.Errorf("enroll: %w", err)
}
log.Printf("enrolled faceID=%d", faceID)
```

The `faceID` is assigned by the device. Persist it in your own user table alongside whatever business identifier (employee ID, account UUID, etc.) maps to that face.

If the device returns `ResultDuplicateFace`, the face is already enrolled — branch on it as shown in [Handling errors](#handling-errors).

### 3. Recognize a face

Symmetric with enrollment: present a face, get back the matched `faceID`.

```go
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()

faceID, err := client.Recognize(ctx)
switch {
case err == nil:
    log.Printf("matched faceID=%d", faceID)
case isResult(err, tx510.ResultNoFace):
    log.Println("no face presented")
case isResult(err, tx510.ResultMatchFailed):
    log.Println("face not enrolled")
default:
    return err
}
```

(`isResult` helper defined in [Handling errors](#handling-errors).)

### 4. List enrolled users

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()

ids, err := client.UserCount(ctx)
if err != nil {
    return err
}
log.Printf("%d enrolled faces: %v", len(ids), ids)
```

Returns `nil, nil` if the gallery is empty.

### 5. Delete a user

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()

if err := client.DeleteUser(ctx, faceID); err != nil {
    return err
}
```

Or wipe the entire gallery (no confirmation on the device — gate this in your application):

```go
if err := client.DeleteAll(ctx); err != nil {
    return err
}
```

### 6. Back up the gallery

Each face is a 1024-byte template ("eigenvalue") you can extract. A useful pattern is to dump all of them into a tarball or JSON sidecar so you can re-provision a replacement device later.

```go
func backup(ctx context.Context, client *tx510.Client, dir string) error {
    ids, err := client.UserCount(ctx)
    if err != nil {
        return fmt.Errorf("list users: %w", err)
    }
    for _, id := range ids {
        template, err := client.ReadTemplate(ctx, id)
        if err != nil {
            return fmt.Errorf("read faceID %d: %w", id, err)
        }
        path := filepath.Join(dir, fmt.Sprintf("%05d.tpl", id))
        if err := os.WriteFile(path, template, 0o644); err != nil {
            return err
        }
    }
    return nil
}
```

Each `ReadTemplate` call takes a couple hundred milliseconds. Don't try to parallelize — `Client` is half-duplex and will serialize you anyway.

### 7. Restore a backup

Upload templates back into the device's gallery. The wire protocol breaks each upload into four 256-byte chunks — `WriteTemplate` handles that for you.

```go
func restore(ctx context.Context, client *tx510.Client, dir string) error {
    entries, err := os.ReadDir(dir)
    if err != nil {
        return err
    }
    for _, e := range entries {
        if filepath.Ext(e.Name()) != ".tpl" {
            continue
        }
        data, err := os.ReadFile(filepath.Join(dir, e.Name()))
        if err != nil {
            return err
        }
        if err := client.WriteTemplate(ctx, data); err != nil {
            return fmt.Errorf("write %s: %w", e.Name(), err)
        }
    }
    return nil
}
```

Note: the device assigns new `faceID`s when restoring; the IDs in the filenames are just for your bookkeeping.

### 8. Change the baud rate

`SetBaudRate` is host-aware — it issues the device-side command and reconfigures the host port to match in one call.

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()

if err := client.SetBaudRate(ctx, tx510.Baud57600); err != nil {
    return err
}
// host port is already at 57600 — keep using `client` as normal
```

The new rate persists on the device across power cycles. Next time you reconnect, pass the matching `WithBaud` to `New`:

```go
client, _ := tx510.New("/dev/ttyUSB0", tx510.WithBaud(57600))
```

If you implemented your own `Transport` via `NewWithTransport`, see [`README.md`](README.md#caveats) — you'll need to either reconfigure the host yourself or implement `Reconfigure(baud int) error` on your Transport.

### 9. Toggle backlight / display / LED

```go
client.SetBacklight(ctx, true)   // turn the LCD backlight on
client.SetDisplay(ctx, false)    // turn the display content off
client.SetWhiteLight(ctx, true)  // turn the white assist LED on for low-light enrollment
```

All three take a context and a bool. They're synchronous — return when the device acknowledges.

### 10. Long-running service

The natural shape for a server that owns the device: open once at startup, close at shutdown, share the `*Client` across handlers.

```go
type AccessService struct {
    client *tx510.Client
}

func NewAccessService(port string) (*AccessService, error) {
    c, err := tx510.New(port)
    if err != nil {
        return nil, err
    }
    return &AccessService{client: c}, nil
}

func (s *AccessService) Close() error { return s.client.Close() }

func (s *AccessService) HandleBadgeIn(w http.ResponseWriter, r *http.Request) {
    ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
    defer cancel()

    faceID, err := s.client.Recognize(ctx)
    if err != nil {
        http.Error(w, err.Error(), http.StatusUnauthorized)
        return
    }
    fmt.Fprintln(w, lookupUser(faceID))
}
```

`*tx510.Client` is safe for concurrent use; calls are serialized internally so the half-duplex device never sees overlapping requests. You don't need any locking of your own.

What to *avoid*:

- Don't open and close the port per request. Opening a serial port is slow (tens of ms) and you'll churn file descriptors.
- Don't `Reboot` from inside a request handler — the device takes ~1–2s to come back, and `client.Reboot` may return a read-timeout error since the module often resets before ACKing.

---

## Handling errors

Every command method returns one of three error kinds:

1. **I/O error** — the wire conversation failed (port disconnected, garbage on the line). Wrapped, looks like `tx510: read recognize reply: ...`.
2. **Frame error** — `errors.Is(err, tx510.ErrBadHeader)` / `ErrParityMismatch` / `ErrFrameTooShort` / `ErrFrameTooLarge`. Usually means baud-rate mismatch.
3. **Device error** — the device ACKed but reported a non-success `ResultCode`. Comes back as a `*tx510.ResultError`.

A useful helper for branching on device-side outcomes:

```go
func isResult(err error, code tx510.ResultCode) bool {
    var rerr *tx510.ResultError
    return errors.As(err, &rerr) && rerr.Code == code
}
```

Then in a handler:

```go
faceID, err := client.Recognize(ctx)
switch {
case err == nil:
    // success path
case errors.Is(err, context.DeadlineExceeded):
    // caller-side timeout — user didn't present a face in time
case isResult(err, tx510.ResultNoFace):
    // device gave up waiting
case isResult(err, tx510.ResultLiveness2DFailed),
     isResult(err, tx510.ResultLiveness3DFailed):
    // anti-spoofing rejected — likely a photo or screen
case isResult(err, tx510.ResultMatchFailed):
    // face not in gallery
default:
    // I/O or framing failure — log and surface
    log.Printf("recognize: %v", err)
}
```

The full list of result codes (with hex values and human strings) is in `codes.go`.

---

## Troubleshooting

| Symptom                                              | Likely cause                                                                                                              |
| ---------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------- |
| `tx510: open ...: no such file or directory`         | Wrong port path. Re-check with the `ls` / Device Manager commands above.                                                  |
| `tx510: open ...: permission denied` (Linux)         | User not in `dialout`/`uucp` group. See [Permissions](#permissions).                                                      |
| `tx510: open ...: device or resource busy`           | Another process has the port open (Arduino IDE serial monitor, an old instance of your service, `minicom`, etc.).         |
| `tx510: bad frame header` / `tx510: parity mismatch` | Baud-rate mismatch between host and device. If you previously changed the rate, pass `WithBaud(...)` to `New`.            |
| `context deadline exceeded` from `Register`/`Recognize` | The caller's `ctx` timeout is too short. Bump to 30s+ for face operations.                                              |
| `Reboot` returns a read-error                        | Expected — the module often resets before ACKing. Treat any error from `Reboot` as "probably succeeded" and wait ~2s.    |
| Commands hang forever                                | Reads aren't deadlined. Make sure your `ctx` has a deadline (`context.WithTimeout`) and don't pass `context.Background()` directly. |
| All commands fail after `SetBaudRate`                | You likely used `NewWithTransport` with a backend that doesn't support `Reconfigure`. Host is still at the old rate.      |

For anything not on this list, run with verbose-ish logging around each call and capture the raw error — it'll be wrapped with the command name (`tx510: write recognize: ...` etc.) so you can tell which step failed.
