package tx510

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// replyTransport is a non-blocking fake. Each Write is followed by Reads that
// return the next pre-loaded reply frame. Used for all happy-path tests.
type replyTransport struct {
	mu        sync.Mutex
	written   bytes.Buffer
	readBuf   bytes.Buffer
	deadlines []time.Time
}

func newReplyTransport(replies ...[]byte) *replyTransport {
	rt := &replyTransport{}
	for _, r := range replies {
		rt.readBuf.Write(r)
	}
	return rt
}

func (r *replyTransport) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.written.Write(p)
}

func (r *replyTransport) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.readBuf.Len() == 0 {
		return 0, io.EOF
	}
	return r.readBuf.Read(p)
}

func (r *replyTransport) SetReadDeadline(t time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deadlines = append(r.deadlines, t)
	return nil
}

func (r *replyTransport) writtenBytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]byte, r.written.Len())
	copy(out, r.written.Bytes())
	return out
}

// makeReply builds a wire-format reply frame.
func makeReply(ackedID CmdID, result ResultCode, payload []byte) []byte {
	data := make([]byte, 0, 2+len(payload))
	data = append(data, byte(ackedID), byte(result))
	data = append(data, payload...)
	return BuildFrame(ackedID, data)
}

// parseRequest extracts (msgID, data) from a request frame. Unlike ParseFrame
// (which assumes reply layout and requires ackedID+result up front), this
// helper just validates magic + size + parity and hands back the raw data.
func parseRequest(t *testing.T, raw []byte) (CmdID, []byte) {
	t.Helper()
	if len(raw) < headerLen+parityLen {
		t.Fatalf("frame too short: % X", raw)
	}
	if raw[0] != magic0 || raw[1] != magic1 {
		t.Fatalf("bad magic: % X", raw[:2])
	}
	size := int(binary.BigEndian.Uint32(raw[3:7]))
	if len(raw) != headerLen+size+parityLen {
		t.Fatalf("frame length mismatch: declared %d data bytes, frame is %d bytes",
			size, len(raw))
	}
	var want byte
	for _, b := range raw[2 : headerLen+size] {
		want += b
	}
	if got := raw[headerLen+size]; got != want {
		t.Fatalf("parity got 0x%02X want 0x%02X", got, want)
	}
	data := make([]byte, size)
	copy(data, raw[headerLen:headerLen+size])
	return CmdID(raw[2]), data
}

// requestData returns the raw data segment of a request frame: everything
// between the size field and the parity byte, with no interpretation as
// reply data.
func requestData(t *testing.T, raw []byte) []byte {
	t.Helper()
	if len(raw) < headerLen+parityLen {
		t.Fatalf("frame too short: % X", raw)
	}
	size := int(binary.BigEndian.Uint32(raw[3:7]))
	if len(raw) != headerLen+size+parityLen {
		t.Fatalf("frame length mismatch: declared %d data bytes, frame is %d bytes", size, len(raw))
	}
	out := make([]byte, size)
	copy(out, raw[headerLen:headerLen+size])
	return out
}

func TestClient_Recognize_Success(t *testing.T) {
	rt := newReplyTransport(makeReply(CmdRecognize, ResultSuccess, []byte{0x00, 0x2A}))
	c := NewWithTransport(rt)

	faceID, err := c.Recognize(context.Background())
	if err != nil {
		t.Fatalf("Recognize: %v", err)
	}
	if faceID != 42 {
		t.Errorf("faceID = %d, want 42", faceID)
	}

	// Request must be a plain Recognize frame with no data.
	id, data := parseRequest(t, rt.writtenBytes())
	if id != CmdRecognize {
		t.Errorf("written msgID = 0x%02X, want CmdRecognize", byte(id))
	}
	if len(data) != 0 {
		t.Errorf("Recognize should send no data, got % X", data)
	}
}

func TestClient_Recognize_DeviceError(t *testing.T) {
	rt := newReplyTransport(makeReply(CmdRecognize, ResultMatchFailed, []byte{0x00, 0x00}))
	c := NewWithTransport(rt)

	_, err := c.Recognize(context.Background())
	var rerr *ResultError
	if !errors.As(err, &rerr) {
		t.Fatalf("got %v (%T), want *ResultError", err, err)
	}
	if rerr.Code != ResultMatchFailed {
		t.Errorf("Code = 0x%02X, want ResultMatchFailed", byte(rerr.Code))
	}
}

func TestClient_Recognize_RetriesOnLiveness2DFailed(t *testing.T) {
	// Two 2D-liveness rejects then success: the retry path returns the third reply's faceID.
	rt := newReplyTransport(
		makeReply(CmdRecognize, ResultLiveness2DFailed, nil),
		makeReply(CmdRecognize, ResultLiveness2DFailed, nil),
		makeReply(CmdRecognize, ResultSuccess, []byte{0x00, 0x2A}),
	)
	c := NewWithTransport(rt)

	faceID, err := c.Recognize(context.Background(), AllowLivenessRetries(2, 0))
	if err != nil {
		t.Fatalf("Recognize with retries: %v", err)
	}
	if faceID != 42 {
		t.Errorf("faceID = %d, want 42", faceID)
	}
}

func TestClient_Recognize_RetriesExhaustedReturnsLastLivenessError(t *testing.T) {
	rt := newReplyTransport(
		makeReply(CmdRecognize, ResultLiveness2DFailed, nil),
		makeReply(CmdRecognize, ResultLiveness2DFailed, nil),
		makeReply(CmdRecognize, ResultLiveness2DFailed, nil),
	)
	c := NewWithTransport(rt)

	_, err := c.Recognize(context.Background(), AllowLivenessRetries(2, 0))
	if err == nil {
		t.Fatalf("want liveness error after retries exhausted, got nil")
	}
	if !IsLivenessError(err) {
		t.Fatalf("got %v, want IsLivenessError=true", err)
	}
}

func TestClient_Recognize_NonLivenessErrorNotRetried(t *testing.T) {
	// Match-failed must not trigger the retry path even with AllowLivenessRetries set.
	rt := newReplyTransport(
		makeReply(CmdRecognize, ResultMatchFailed, []byte{0x00, 0x00}),
		makeReply(CmdRecognize, ResultSuccess, []byte{0x00, 0x2A}),
	)
	c := NewWithTransport(rt)

	_, err := c.Recognize(context.Background(), AllowLivenessRetries(3, 0))
	if err == nil {
		t.Fatalf("want match-failed error, got nil")
	}
	if IsLivenessError(err) {
		t.Fatalf("got %v, did not want a liveness error", err)
	}
}

func TestIsLivenessError(t *testing.T) {
	if !IsLivenessError(&ResultError{Code: ResultLiveness2DFailed}) {
		t.Errorf("ResultLiveness2DFailed should be a liveness error")
	}
	if !IsLivenessError(&ResultError{Code: ResultLiveness3DFailed}) {
		t.Errorf("ResultLiveness3DFailed should be a liveness error")
	}
	if IsLivenessError(&ResultError{Code: ResultMatchFailed}) {
		t.Errorf("ResultMatchFailed is not a liveness error")
	}
	if IsLivenessError(nil) {
		t.Errorf("nil error must not be a liveness error")
	}
	if IsLivenessError(errors.New("io: some unrelated failure")) {
		t.Errorf("plain errors must not be a liveness error")
	}
}

func TestClient_Recognize_ShortPayload(t *testing.T) {
	// Payload omits the faceID bytes.
	rt := newReplyTransport(makeReply(CmdRecognize, ResultSuccess, []byte{0x01}))
	c := NewWithTransport(rt)

	if _, err := c.Recognize(context.Background()); err == nil {
		t.Fatalf("expected error for short payload, got nil")
	}
}

func TestClient_Register_Success(t *testing.T) {
	rt := newReplyTransport(makeReply(CmdRegister, ResultSuccess, []byte{0x01, 0x00}))
	c := NewWithTransport(rt)

	faceID, err := c.Register(context.Background())
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if faceID != 256 {
		t.Errorf("faceID = %d, want 256", faceID)
	}
}

func TestClient_DeleteUser_EncodesFaceID(t *testing.T) {
	rt := newReplyTransport(makeReply(CmdDeleteUser, ResultSuccess, nil))
	c := NewWithTransport(rt)

	if err := c.DeleteUser(context.Background(), 0xBEEF); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}

	data := requestData(t, rt.writtenBytes())
	if len(data) != 2 || data[0] != 0xBE || data[1] != 0xEF {
		t.Errorf("request data = % X, want BE EF", data)
	}
}

func TestClient_DeleteAll(t *testing.T) {
	rt := newReplyTransport(makeReply(CmdDeleteAll, ResultSuccess, nil))
	c := NewWithTransport(rt)

	if err := c.DeleteAll(context.Background()); err != nil {
		t.Fatalf("DeleteAll: %v", err)
	}
	if data := requestData(t, rt.writtenBytes()); len(data) != 0 {
		t.Errorf("DeleteAll should send no data, got % X", data)
	}
}

func TestClient_UserCount_Empty(t *testing.T) {
	rt := newReplyTransport(makeReply(CmdUserCount, ResultSuccess, []byte{0x00, 0x00}))
	c := NewWithTransport(rt)

	ids, err := c.UserCount(context.Background())
	if err != nil {
		t.Fatalf("UserCount: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("ids = %v, want empty", ids)
	}
}

func TestClient_UserCount_WithIDs(t *testing.T) {
	payload := []byte{0x00, 0x03, 0x00, 0x01, 0x00, 0x02, 0x10, 0x00}
	rt := newReplyTransport(makeReply(CmdUserCount, ResultSuccess, payload))
	c := NewWithTransport(rt)

	ids, err := c.UserCount(context.Background())
	if err != nil {
		t.Fatalf("UserCount: %v", err)
	}
	want := []uint16{1, 2, 0x1000}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Errorf("ids[%d] = %d, want %d", i, ids[i], want[i])
		}
	}
}

func TestClient_UserCount_DeclaredLengthMismatch(t *testing.T) {
	// Declares 4 IDs but only carries 1.
	payload := []byte{0x00, 0x04, 0x00, 0x01}
	rt := newReplyTransport(makeReply(CmdUserCount, ResultSuccess, payload))
	c := NewWithTransport(rt)

	if _, err := c.UserCount(context.Background()); err == nil {
		t.Fatalf("expected mismatch error, got nil")
	}
}

func TestClient_Version_TrimsTrailingNulls(t *testing.T) {
	payload := append([]byte("HLK-TX510 v1.2.3"), 0x00, 0x00, 0x00)
	rt := newReplyTransport(makeReply(CmdVersion, ResultSuccess, payload))
	c := NewWithTransport(rt)

	got, err := c.Version(context.Background())
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if got != "HLK-TX510 v1.2.3" {
		t.Errorf("Version = %q, want trailing NULs trimmed", got)
	}
}

func TestClient_Reboot(t *testing.T) {
	rt := newReplyTransport(makeReply(CmdReboot, ResultSuccess, nil))
	c := NewWithTransport(rt)

	if err := c.Reboot(context.Background()); err != nil {
		t.Fatalf("Reboot: %v", err)
	}
}

func TestClient_Toggle_Commands(t *testing.T) {
	cases := []struct {
		name string
		cmd  CmdID
		call func(c *Client, ctx context.Context, on bool) error
	}{
		{"backlight", CmdBacklight, (*Client).SetBacklight},
		{"display", CmdDisplay, (*Client).SetDisplay},
		{"whitelight", CmdWhiteLight, (*Client).SetWhiteLight},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/on", func(t *testing.T) {
			rt := newReplyTransport(makeReply(tc.cmd, ResultSuccess, nil))
			c := NewWithTransport(rt)
			if err := tc.call(c, context.Background(), true); err != nil {
				t.Fatalf("call: %v", err)
			}
			data := requestData(t, rt.writtenBytes())
			if len(data) != 1 || data[0] != 0x01 {
				t.Errorf("data = % X, want 01", data)
			}
		})
		t.Run(tc.name+"/off", func(t *testing.T) {
			rt := newReplyTransport(makeReply(tc.cmd, ResultSuccess, nil))
			c := NewWithTransport(rt)
			if err := tc.call(c, context.Background(), false); err != nil {
				t.Fatalf("call: %v", err)
			}
			data := requestData(t, rt.writtenBytes())
			if len(data) != 1 || data[0] != 0x00 {
				t.Errorf("data = % X, want 00", data)
			}
		})
	}
}

func TestClient_SetBaudRate(t *testing.T) {
	rt := newReplyTransport(makeReply(CmdBaudRate, ResultSuccess, nil))
	c := NewWithTransport(rt)

	if err := c.SetBaudRate(context.Background(), Baud57600); err != nil {
		t.Fatalf("SetBaudRate: %v", err)
	}

	data := requestData(t, rt.writtenBytes())
	if len(data) != 1 || data[0] != byte(Baud57600) {
		t.Errorf("data = % X, want %02X", data, byte(Baud57600))
	}
}

func TestClient_ReadTemplate_Success(t *testing.T) {
	// Payload: rand(1) seq(1) faceID(2) template(1024)
	template := bytes.Repeat([]byte{0xA5}, TemplateSize)
	payload := append([]byte{0x42, 0x0F, 0x00, 0x07}, template...)
	rt := newReplyTransport(makeReply(CmdReadEigenvalue, ResultSuccess, payload))
	c := NewWithTransport(rt)

	got, err := c.ReadTemplate(context.Background(), 7)
	if err != nil {
		t.Fatalf("ReadTemplate: %v", err)
	}
	if !bytes.Equal(got, template) {
		t.Errorf("template mismatch (len=%d)", len(got))
	}

	// Sanity-check the request: rand | faceID hi | faceID lo | seq
	data := requestData(t, rt.writtenBytes())
	want := []byte{0x42, 0x00, 0x07, 0x0F}
	if !bytes.Equal(data, want) {
		t.Errorf("request data = % X, want % X", data, want)
	}
}

func TestClient_ReadTemplate_PayloadTooShort(t *testing.T) {
	// Header present but template truncated.
	short := append([]byte{0x42, 0x0F, 0x00, 0x07}, bytes.Repeat([]byte{0x00}, 100)...)
	rt := newReplyTransport(makeReply(CmdReadEigenvalue, ResultSuccess, short))
	c := NewWithTransport(rt)

	if _, err := c.ReadTemplate(context.Background(), 7); err == nil {
		t.Fatalf("expected short-payload error, got nil")
	}
}

func TestClient_WriteTemplate_HappyPath(t *testing.T) {
	template := make([]byte, TemplateSize)
	for i := range template {
		template[i] = byte(i)
	}

	// Replies for the four chunks; the second byte of each payload is the seq ack.
	ackSeqs := []byte{0x01, 0x03, 0x07, 0x0F}
	replies := make([][]byte, 4)
	for i, seq := range ackSeqs {
		// Payload: rand | seq_ack | faceID(2)
		replies[i] = makeReply(CmdWriteEigenvalue, ResultSuccess, []byte{0x42, seq, 0x00, 0x00})
	}
	rt := newReplyTransport(replies...)
	c := NewWithTransport(rt)

	if err := c.WriteTemplate(context.Background(), template); err != nil {
		t.Fatalf("WriteTemplate: %v", err)
	}

	// Verify we wrote exactly four frames, each carrying [rand, sendSeq, 256 bytes].
	wantSeqs := []byte{0x01, 0x02, 0x04, 0x08}
	raw := rt.writtenBytes()
	for i := 0; i < 4; i++ {
		if len(raw) < headerLen+parityLen {
			t.Fatalf("ran out of bytes parsing chunk %d", i+1)
		}
		size := int(binary.BigEndian.Uint32(raw[3:7]))
		frameLen := headerLen + size + parityLen
		if len(raw) < frameLen {
			t.Fatalf("chunk %d: truncated frame", i+1)
		}

		data := raw[headerLen : headerLen+size]
		raw = raw[frameLen:]

		if len(data) != 2+256 {
			t.Errorf("chunk %d: data len = %d, want %d", i+1, len(data), 2+256)
			continue
		}
		if data[0] != 0x42 {
			t.Errorf("chunk %d: rand = 0x%02X, want 0x42", i+1, data[0])
		}
		if data[1] != wantSeqs[i] {
			t.Errorf("chunk %d: sendSeq = 0x%02X, want 0x%02X", i+1, data[1], wantSeqs[i])
		}
		if !bytes.Equal(data[2:], template[i*256:(i+1)*256]) {
			t.Errorf("chunk %d: payload slice mismatch", i+1)
		}
	}
	if len(raw) != 0 {
		t.Errorf("extra bytes after 4 chunks: % X", raw)
	}
}

func TestClient_WriteTemplate_WrongLength(t *testing.T) {
	c := NewWithTransport(newReplyTransport()) // no replies needed — should fail before any I/O
	err := c.WriteTemplate(context.Background(), make([]byte, 100))
	if err == nil {
		t.Fatalf("expected length error, got nil")
	}
}

func TestClient_WriteTemplate_BadAckSeq(t *testing.T) {
	template := make([]byte, TemplateSize)
	// First chunk gets the wrong ack seq.
	replies := []byte{}
	_ = replies
	bad := makeReply(CmdWriteEigenvalue, ResultSuccess, []byte{0x42, 0xFF, 0x00, 0x00})
	rt := newReplyTransport(bad)
	c := NewWithTransport(rt)

	err := c.WriteTemplate(context.Background(), template)
	if err == nil {
		t.Fatalf("expected ack-seq error, got nil")
	}
}

func TestClient_WriteTemplate_DeviceErrorOnChunk(t *testing.T) {
	template := make([]byte, TemplateSize)
	// First chunk succeeds, second returns a device error.
	rt := newReplyTransport(
		makeReply(CmdWriteEigenvalue, ResultSuccess, []byte{0x42, 0x01, 0x00, 0x00}),
		makeReply(CmdWriteEigenvalue, ResultLiveness3DFailed, []byte{0x42, 0x03, 0x00, 0x00}),
	)
	c := NewWithTransport(rt)

	err := c.WriteTemplate(context.Background(), template)
	if err == nil {
		t.Fatalf("expected device-error wrap, got nil")
	}
	var rerr *ResultError
	if !errors.As(err, &rerr) {
		t.Fatalf("got %v, want wrapped *ResultError", err)
	}
}

// blockingTransport blocks on Read until SetReadDeadline is called with a
// non-future deadline. Used to verify context-cancellation behavior.
type blockingTransport struct {
	mu      sync.Mutex
	abort   chan struct{}
	written bytes.Buffer
}

func newBlockingTransport() *blockingTransport {
	return &blockingTransport{abort: make(chan struct{})}
}

func (b *blockingTransport) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.written.Write(p)
}

func (b *blockingTransport) Read(p []byte) (int, error) {
	<-b.abort
	return 0, errors.New("blocking transport: read aborted")
}

func (b *blockingTransport) SetReadDeadline(t time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !t.After(time.Now()) {
		select {
		case <-b.abort:
		default:
			close(b.abort)
		}
	}
	return nil
}

func TestClient_ContextCancellation_AbortsRead(t *testing.T) {
	bt := newBlockingTransport()
	c := NewWithTransport(bt)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.Version(ctx)
		done <- err
	}()

	// Give the call a moment to enter the blocking Read.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("call did not return after cancel")
	}
}

func TestClient_SerializesConcurrentCalls(t *testing.T) {
	// Two replies preloaded; two goroutines call methods concurrently. Both must
	// succeed without garbling each other on the wire.
	rt := newReplyTransport(
		makeReply(CmdVersion, ResultSuccess, []byte("v1")),
		makeReply(CmdVersion, ResultSuccess, []byte("v1")),
	)
	c := NewWithTransport(rt)

	var wg sync.WaitGroup
	wg.Add(2)
	errCh := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			if _, err := c.Version(context.Background()); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent Version: %v", err)
	}
}

func TestClient_DumpAll_WritesOneFilePerFace(t *testing.T) {
	tplA := bytes.Repeat([]byte{0xA5}, TemplateSize)
	tplB := bytes.Repeat([]byte{0x5A}, TemplateSize)
	rt := newReplyTransport(
		makeReply(CmdUserCount, ResultSuccess, []byte{0x00, 0x02, 0x00, 0x07, 0x00, 0x2A}),
		makeReply(CmdReadEigenvalue, ResultSuccess, append([]byte{0x42, 0x0F, 0x00, 0x07}, tplA...)),
		makeReply(CmdReadEigenvalue, ResultSuccess, append([]byte{0x42, 0x0F, 0x00, 0x2A}, tplB...)),
	)
	c := NewWithTransport(rt)

	dir := t.TempDir()
	ids, err := c.DumpAll(context.Background(), dir)
	if err != nil {
		t.Fatalf("DumpAll: %v", err)
	}
	if len(ids) != 2 || ids[0] != 7 || ids[1] != 42 {
		t.Errorf("ids = %v, want [7 42]", ids)
	}

	gotA, err := os.ReadFile(filepath.Join(dir, "00007.tpl"))
	if err != nil {
		t.Fatalf("read dumped file A: %v", err)
	}
	if !bytes.Equal(gotA, tplA) {
		t.Errorf("dumped file 00007.tpl does not match source template")
	}
	gotB, err := os.ReadFile(filepath.Join(dir, "00042.tpl"))
	if err != nil {
		t.Fatalf("read dumped file B: %v", err)
	}
	if !bytes.Equal(gotB, tplB) {
		t.Errorf("dumped file 00042.tpl does not match source template")
	}
}

func TestClient_DumpAll_EmptyGallery(t *testing.T) {
	rt := newReplyTransport(makeReply(CmdUserCount, ResultSuccess, []byte{0x00, 0x00}))
	c := NewWithTransport(rt)

	dir := t.TempDir()
	ids, err := c.DumpAll(context.Background(), dir)
	if err != nil {
		t.Fatalf("DumpAll empty: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("ids = %v, want empty", ids)
	}
}

func TestClient_RestoreAll_SkipsNonTplAndSortsFilenames(t *testing.T) {
	dir := t.TempDir()
	tplA := bytes.Repeat([]byte{0xA5}, TemplateSize)
	tplB := bytes.Repeat([]byte{0x5A}, TemplateSize)
	if err := os.WriteFile(filepath.Join(dir, "00002.tpl"), tplB, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "00001.tpl"), tplA, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("ignore me"), 0o644); err != nil {
		t.Fatal(err)
	}

	ackSeqs := []byte{0x01, 0x03, 0x07, 0x0F}
	replies := make([][]byte, 0, 8)
	for range 2 {
		for _, seq := range ackSeqs {
			replies = append(replies, makeReply(CmdWriteEigenvalue, ResultSuccess, []byte{0x42, seq, 0x00, 0x00}))
		}
	}
	rt := newReplyTransport(replies...)
	c := NewWithTransport(rt)

	n, err := c.RestoreAll(context.Background(), dir)
	if err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}
	if n != 2 {
		t.Errorf("restored count = %d, want 2", n)
	}

	// First 4 chunks must carry tplA's bytes (sorted filename 00001.tpl first), next 4 tplB.
	raw := rt.writtenBytes()
	var firstSlice, secondSlice []byte
	for i := 0; i < 8; i++ {
		if len(raw) < headerLen+parityLen {
			t.Fatalf("ran out of bytes parsing chunk %d", i+1)
		}
		size := int(binary.BigEndian.Uint32(raw[3:7]))
		frameLen := headerLen + size + parityLen
		if len(raw) < frameLen {
			t.Fatalf("chunk %d truncated", i+1)
		}
		data := raw[headerLen : headerLen+size]
		raw = raw[frameLen:]
		chunk := data[2:]
		if i < 4 {
			firstSlice = append(firstSlice, chunk...)
		} else {
			secondSlice = append(secondSlice, chunk...)
		}
	}
	if !bytes.Equal(firstSlice, tplA) {
		t.Errorf("first restored template != tplA (00001.tpl)")
	}
	if !bytes.Equal(secondSlice, tplB) {
		t.Errorf("second restored template != tplB (00002.tpl)")
	}
}

func TestClient_RestoreAll_MissingDir(t *testing.T) {
	c := NewWithTransport(newReplyTransport())
	_, err := c.RestoreAll(context.Background(), filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatalf("expected error for missing dir, got nil")
	}
}

func TestClient_DumpAll_PropagatesReadTemplateError(t *testing.T) {
	rt := newReplyTransport(
		makeReply(CmdUserCount, ResultSuccess, []byte{0x00, 0x01, 0x00, 0x07}),
		makeReply(CmdReadEigenvalue, ResultMatchFailed, nil),
	)
	c := NewWithTransport(rt)

	_, err := c.DumpAll(context.Background(), t.TempDir())
	if err == nil {
		t.Fatalf("expected ReadTemplate failure to surface, got nil")
	}
	var rerr *ResultError
	if !errors.As(err, &rerr) || rerr.Code != ResultMatchFailed {
		t.Errorf("got %v, want wrapped *ResultError{ResultMatchFailed}", err)
	}
}
