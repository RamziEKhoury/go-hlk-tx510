package tx510

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestBuildFrame_NoData(t *testing.T) {
	got := BuildFrame(CmdRegister, nil)
	// magic(EF AA) | msgID(13) | size(00 00 00 00) | parity(13)
	want := []byte{0xEF, 0xAA, 0x13, 0x00, 0x00, 0x00, 0x00, 0x13}
	if !bytes.Equal(got, want) {
		t.Fatalf("frame mismatch\n got: % X\nwant: % X", got, want)
	}
}

func TestBuildFrame_WithData(t *testing.T) {
	got := BuildFrame(CmdVersion, []byte{0x01, 0x02, 0x03})
	// parity = 0x30 + 0 + 0 + 0 + 0x03 + 0x01 + 0x02 + 0x03 = 0x39
	want := []byte{0xEF, 0xAA, 0x30, 0x00, 0x00, 0x00, 0x03, 0x01, 0x02, 0x03, 0x39}
	if !bytes.Equal(got, want) {
		t.Fatalf("frame mismatch\n got: % X\nwant: % X", got, want)
	}
}

func TestBuildFrame_ParityWrapsModulo256(t *testing.T) {
	// Pick a data slice whose bytes plus the header force the parity to overflow.
	data := bytes.Repeat([]byte{0xFF}, 4)
	frame := BuildFrame(CmdReadEigenvalue, data) // msgID 0xC6
	parity := frame[len(frame)-1]
	// Expected: 0xC6 + 0 + 0 + 0 + 0x04 + 0xFF*4 = 0xC6 + 4 + 0x3FC = 0x4C6  -> low byte 0xC6
	if parity != 0xC6 {
		t.Fatalf("parity = 0x%02X, want 0xC6 (mod-256 sum)", parity)
	}
}

func TestParseFrame_HappyPath(t *testing.T) {
	// Reply data = [ackedID=0x12, result=0x00, faceID=0x00 0x05]
	data := []byte{byte(CmdRecognize), byte(ResultSuccess), 0x00, 0x05}
	raw := BuildFrame(CmdRecognize, data)

	reply, err := ParseFrame(raw)
	if err != nil {
		t.Fatalf("ParseFrame: %v", err)
	}
	if reply.AckedID != CmdRecognize {
		t.Errorf("AckedID = 0x%02X, want 0x%02X", byte(reply.AckedID), byte(CmdRecognize))
	}
	if reply.Result != ResultSuccess {
		t.Errorf("Result = 0x%02X, want Success", byte(reply.Result))
	}
	if !bytes.Equal(reply.Payload, []byte{0x00, 0x05}) {
		t.Errorf("Payload = % X, want 00 05", reply.Payload)
	}
}

func TestParseFrame_TooShort(t *testing.T) {
	cases := map[string][]byte{
		"empty":         nil,
		"only magic":    {0xEF, 0xAA},
		"missing parity": {0xEF, 0xAA, 0x12, 0x00, 0x00, 0x00, 0x00}, // header but no parity byte
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseFrame(raw); !errors.Is(err, ErrFrameTooShort) {
				t.Fatalf("got %v, want ErrFrameTooShort", err)
			}
		})
	}
}

func TestParseFrame_BadHeader(t *testing.T) {
	raw := []byte{0xDE, 0xAD, 0x12, 0x00, 0x00, 0x00, 0x00, 0x00}
	if _, err := ParseFrame(raw); !errors.Is(err, ErrBadHeader) {
		t.Fatalf("got %v, want ErrBadHeader", err)
	}
}

func TestParseFrame_TooLarge(t *testing.T) {
	// size field declares MaxFrameSize+1.
	raw := []byte{0xEF, 0xAA, 0x12, 0x00, 0x00, 0x10, 0x01, 0x00}
	if _, err := ParseFrame(raw); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("got %v, want ErrFrameTooLarge", err)
	}
}

func TestParseFrame_TruncatedBody(t *testing.T) {
	// Declares 8 bytes of data but only carries 2.
	raw := []byte{0xEF, 0xAA, 0x12, 0x00, 0x00, 0x00, 0x08, 0x12, 0x00, 0x00}
	if _, err := ParseFrame(raw); !errors.Is(err, ErrFrameTooShort) {
		t.Fatalf("got %v, want ErrFrameTooShort wrap", err)
	}
}

func TestParseFrame_ParityMismatch(t *testing.T) {
	raw := BuildFrame(CmdRecognize, []byte{byte(CmdRecognize), byte(ResultSuccess), 0x00, 0x05})
	raw[len(raw)-1] ^= 0xFF // corrupt parity

	if _, err := ParseFrame(raw); !errors.Is(err, ErrParityMismatch) {
		t.Fatalf("got %v, want ErrParityMismatch", err)
	}
}

func TestParseFrame_ReplyDataTooShort(t *testing.T) {
	// size=1, so only one byte of data — not enough for ackedID+result.
	raw := []byte{0xEF, 0xAA, 0x12, 0x00, 0x00, 0x00, 0x01, 0x12}
	// parity = msgID + size + data = 0x12 + 1 + 0x12 = 0x25
	raw = append(raw, 0x25)

	if _, err := ParseFrame(raw); !errors.Is(err, ErrFrameTooShort) {
		t.Fatalf("got %v, want ErrFrameTooShort", err)
	}
}

func TestReadFrame_HappyPath(t *testing.T) {
	data := []byte{byte(CmdVersion), byte(ResultSuccess), 'v', '1', '.', '0'}
	raw := BuildFrame(CmdVersion, data)

	reply, err := ReadFrame(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if reply.AckedID != CmdVersion {
		t.Errorf("AckedID = 0x%02X, want 0x%02X", byte(reply.AckedID), byte(CmdVersion))
	}
	if string(reply.Payload) != "v1.0" {
		t.Errorf("Payload = %q, want %q", reply.Payload, "v1.0")
	}
}

func TestReadFrame_ShortHeader(t *testing.T) {
	_, err := ReadFrame(bytes.NewReader([]byte{0xEF}))
	if err == nil || !strings.Contains(err.Error(), "read header") {
		t.Fatalf("got %v, want a read-header error", err)
	}
}

func TestReadFrame_TooLarge(t *testing.T) {
	hdr := []byte{0xEF, 0xAA, 0x12, 0x00, 0x00, 0x10, 0x01}
	if _, err := ReadFrame(bytes.NewReader(hdr)); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("got %v, want ErrFrameTooLarge", err)
	}
}

func TestReadFrame_BodyTruncated(t *testing.T) {
	// Header says 4 bytes of data + parity, but supply only 2.
	raw := []byte{0xEF, 0xAA, 0x12, 0x00, 0x00, 0x00, 0x04, 0x12, 0x00}
	_, err := ReadFrame(bytes.NewReader(raw))
	if err == nil || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("got %v, want wrapped io.ErrUnexpectedEOF", err)
	}
}

func TestReply_Err(t *testing.T) {
	if (&Reply{Result: ResultSuccess}).Err() != nil {
		t.Errorf("ResultSuccess should produce nil error")
	}

	r := &Reply{Result: ResultNoFace}
	err := r.Err()
	if err == nil {
		t.Fatalf("non-success reply should return error")
	}

	var rerr *ResultError
	if !errors.As(err, &rerr) {
		t.Fatalf("Err() = %T, want *ResultError", err)
	}
	if rerr.Code != ResultNoFace {
		t.Errorf("rerr.Code = 0x%02X, want 0x%02X", byte(rerr.Code), byte(ResultNoFace))
	}
}

func TestResultError_Error_FormatsCode(t *testing.T) {
	err := &ResultError{Code: ResultMatchFailed}
	msg := err.Error()
	// Must contain the code's human name and hex form so logs are searchable both ways.
	if !strings.Contains(msg, "tx510") {
		t.Errorf("error string %q missing 'tx510' prefix", msg)
	}
	if !strings.Contains(msg, "0x08") {
		t.Errorf("error string %q missing hex code 0x08", msg)
	}
}

func TestResultCode_String_KnownAndUnknown(t *testing.T) {
	if ResultSuccess.String() != "Success" {
		t.Errorf("ResultSuccess.String() = %q, want %q", ResultSuccess.String(), "Success")
	}
	unknown := ResultCode(0xFE).String()
	if !strings.Contains(unknown, "0xfe") && !strings.Contains(unknown, "0xFE") {
		t.Errorf("unknown code stringified as %q, want it to include 0xfe", unknown)
	}
}

func TestResultCode_IsSuccess(t *testing.T) {
	if !ResultSuccess.IsSuccess() {
		t.Errorf("ResultSuccess.IsSuccess() = false")
	}
	if ResultNoFace.IsSuccess() {
		t.Errorf("ResultNoFace.IsSuccess() = true")
	}
}

func TestBuildParse_RoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		ackedID CmdID
		result  ResultCode
		payload []byte
	}{
		{"success no payload", CmdReboot, ResultSuccess, nil},
		{"success short payload", CmdRecognize, ResultSuccess, []byte{0x00, 0x07}},
		{"failure", CmdRegister, ResultDuplicateFace, []byte{0x00, 0x01}},
		{"binary payload", CmdReadEigenvalue, ResultSuccess, bytes.Repeat([]byte{0xAB}, 64)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := append([]byte{byte(tc.ackedID), byte(tc.result)}, tc.payload...)
			frame := BuildFrame(tc.ackedID, data)

			reply, err := ParseFrame(frame)
			if err != nil {
				t.Fatalf("ParseFrame: %v", err)
			}
			if reply.AckedID != tc.ackedID {
				t.Errorf("AckedID = 0x%02X, want 0x%02X", byte(reply.AckedID), byte(tc.ackedID))
			}
			if reply.Result != tc.result {
				t.Errorf("Result = 0x%02X, want 0x%02X", byte(reply.Result), byte(tc.result))
			}
			if !bytes.Equal(reply.Payload, tc.payload) {
				t.Errorf("Payload = % X, want % X", reply.Payload, tc.payload)
			}
		})
	}
}
