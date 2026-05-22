package tx510

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	magic0       = 0xEF
	magic1       = 0xAA
	headerLen    = 7 // magic(2) + msgID(1) + size(4)
	parityLen    = 1
	minReplyData = 2 // ackedID(1) + result(1)

	MaxFrameSize = 4096
)

var (
	ErrBadHeader      = errors.New("tx510: bad frame header")
	ErrParityMismatch = errors.New("tx510: parity mismatch")
	ErrFrameTooShort  = errors.New("tx510: frame too short")
	ErrFrameTooLarge  = errors.New("tx510: frame size exceeds maximum")
)

type Reply struct {
	AckedID CmdID
	Result ResultCode
	Payload []byte
}

func (r *Reply) Err() error {
	if r.Result == ResultSuccess {
		return nil
	}
	return &ResultError{Code: r.Result}
}

type ResultError struct {
	Code ResultCode
}

func (e *ResultError) Error() string {
	return fmt.Sprintf("tx510: %s (0x%02x)", e.Code.String(), byte(e.Code))
}


// frame functions: build , parse, read.
func BuildFrame (msgID CmdID, data []byte) []byte{
	size := uint32(len(data))
	buf := make([]byte, 0, headerLen+len(data)+parityLen)
	buf = append(buf, magic0, magic1, byte(msgID))
	buf = binary.BigEndian.AppendUint32(buf, size)
	buf = append(buf, data...)

	var parity byte
	for _, b := range buf[2:] {
		parity += b
	}
	return append(buf,parity)
}

func ParseFrame(raw []byte) (*Reply, error){
	if len(raw) < headerLen+parityLen{
		return nil, ErrFrameTooShort
	}
	if raw[0] != magic0 || raw[1] != magic1 {
		return nil, ErrBadHeader
	}
	size := binary.BigEndian.Uint32(raw[3:7])
	if size > MaxFrameSize{
		return nil, ErrFrameTooLarge
	}
	end := headerLen + int(size)
	if len(raw) < end+parityLen {
		return nil, fmt.Errorf("tx510: declared %d data bytes, have %d: %w",
			size, len(raw)-headerLen-parityLen, ErrFrameTooShort)
	}

	var want byte
	for _, b := range raw[2:end] {
		want += b
	}
	if got := raw[end]; got != want {
		return nil, fmt.Errorf("tx510: parity got 0x%02x want 0x%02x: %w",
			got, want, ErrParityMismatch)
	}

	data := raw[headerLen:end]
	if len(data) < minReplyData {
		return nil, fmt.Errorf("tx510: reply data %d bytes, need at least %d: %w",
			len(data), minReplyData, ErrFrameTooShort)
	}

	payload := make([]byte, len(data)-minReplyData)
	copy(payload, data[minReplyData:])

	return &Reply{
		AckedID: CmdID(data[0]),
		Result:  ResultCode(data[1]),
		Payload: payload,
	}, nil
}

func ReadFrame(r io.Reader) (*Reply, error) {
	hdr := make([]byte, headerLen)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, fmt.Errorf("tx510: read header: %w", err)
	}
	if hdr[0] != magic0 || hdr[1] != magic1 {
		return nil, ErrBadHeader
	}
	size := binary.BigEndian.Uint32(hdr[3:7])
	if size > MaxFrameSize {
		return nil, ErrFrameTooLarge
	}

	full := make([]byte, headerLen+int(size)+parityLen)
	copy(full, hdr)
	if _, err := io.ReadFull(r, full[headerLen:]); err != nil {
		return nil, fmt.Errorf("tx510: read body: %w", err)
	}
	return ParseFrame(full)
}
