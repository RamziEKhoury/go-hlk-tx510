# HLK-TX510 Command Reference

Frame layout (every message): `[0xEF 0xAA][MsgID:1][Size:4 BE][Data:N][Parity:1]`
`BuildFrame` fills in the sync word, size, and parity. **You only choose the MsgID and the Data.**

---

## Quick map: command → MsgID → data

| Command | MsgID | Data to send | Data length |
|---|---|---|---|
| Recognize | `0x12` | none | 0 |
| Register | `0x13` | none | 0 |
| Delete user | `0x20` | faceID (2 bytes, big-endian) | 2 |
| Delete all | `0x21` | none | 0 |
| Version | `0x30` | none | 0 |
| Baud rate | `0x51` | 1 byte: baud code (see options) | 1 |
| Backlight | `0xC0` | 1 byte: `00`=off `01`=on | 1 |
| Display | `0xC1` | 1 byte: `00`=off `01`=on | 1 |
| White light | `0xC2` | 1 byte: `00`=off `01`=on | 1 |
| Reboot | `0xC3` | none | 0 |
| User count | `0xC4` | none | 0 |
| Write template | `0xC5` | rand(1) + seq(1) + feature(256) ×4 | 258 per chunk |
| Read template | `0xC6` | rand(1) + faceID(2 BE) + seq(1) | 4 |

---

## Option values

### Baud rate codes (data byte for `0x51`)
| Code | Baud |
|---|---|
| `0x00` | 9600 |
| `0x01` | 19200 |
| `0x02` | 38400 |
| `0x03` | 57600 |
| `0x04` | 115200 (default) |
> Takes effect only after a reboot; the host must switch its own port to match.

### On/off toggles (data byte for `0xC0`, `0xC1`, `0xC2`)
| Code | Meaning |
|---|---|
| `0x00` | off |
| `0x01` | on |

### Template `seq` byte
**Write (`0xC5`)** — send four chunks in this order:
| Chunk | seq sent | device ack (cumulative) |
|---|---|---|
| 1 | `0x01` | `0x01` |
| 2 | `0x02` | `0x03` |
| 3 | `0x04` | `0x07` |
| 4 | `0x08` | `0x0F` ← commits to flash |

**Read (`0xC6`)**:
| seq | meaning |
|---|---|
| `0x01` | read packet 1 (256 B) |
| `0x02` | read packet 2 |
| `0x04` | read packet 3 |
| `0x08` | read packet 4 |
| `0x0F` | read whole template at once (1024 B) |

> `rand` is any byte you pick (e.g. `0x42`); the device echoes it back so you can match a reply to its request.

---

## Reply result codes

### Recognize / Register (and shown in their ACK)
| Code | Meaning |
|---|---|
| `0x00` | Success (faceID follows) |
| `0x01` | No face detected |
| `0x03` | Face pose angle too large |
| `0x06` | 2D liveness failed |
| `0x07` | 3D liveness failed |
| `0x08` | Match failed |

### Write template (`0xC5`)
| Code | Meaning |
|---|---|
| `0x00` | Success (only meaningful once seq ack = `0x0F`) |
| `0x01` | Fail |
| `0x09` | Face duplication |

### Most other commands
`0x00` = success, `0x01` = fail.

---

## What comes back (reply payload, after the result byte)

| Command | Reply payload |
|---|---|
| Recognize / Register | faceID (2 bytes BE) |
| Delete user / all, toggles, baud, reboot | nothing beyond result |
| Version | ASCII string, e.g. `V1.00.00` |
| User count | count (2 BE) + faceID₁ … faceIDₙ (2 BE each) |
| Write template | rand(1) + seq(1) + faceID(2 BE) |
| Read template | rand(1) + faceID(2 BE) + seq(1) + feature(256 or 1024) |

---

## Build examples

```go
BuildFrame(CmdRecognize, nil)                 // EF AA 12 00 00 00 00 12
BuildFrame(CmdBacklight, []byte{0x01})        // backlight on
BuildFrame(CmdBaudRate, []byte{0x04})         // set 115200
BuildFrame(CmdDeleteUser, []byte{0x00, 0x07}) // delete user 7

// faceID safely from a number:
data := make([]byte, 2)
binary.BigEndian.PutUint16(data, faceID)
BuildFrame(CmdDeleteUser, data)
```

*Source: HLK-TX510 User Manual V1.0 (rev V1.1), §5.1–§5.14.*
