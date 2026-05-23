package tx510

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const DefaultTimeout = 5 * time.Second

type Client struct {
	t      Transport
	closer io.Closer
	mu     sync.Mutex
}

func NewWithTransport(t Transport) *Client {
	return &Client{t: t}
}

func (c *Client) Close() error {
	if c.closer == nil {
		return nil
	}
	return c.closer.Close()
}

type reconfigurer interface {
	Reconfigure(baud int) error
}

func (c *Client) do(ctx context.Context, cmd CmdID, data []byte) (*Reply, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(DefaultTimeout)
	}
	if err := c.t.SetReadDeadline(deadline); err != nil {
		return nil, fmt.Errorf("tx510: set read deadline: %w", err)
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = c.t.SetReadDeadline(time.Now())
		case <-stop:
		}
	}()

	// Drop stale RX bytes (boot noise, leftover from a timed-out call) before writing.
	if f, ok := c.t.(Flusher); ok {
		_ = f.Flush()
	}

	frame := BuildFrame(cmd, data)
	if _, err := c.t.Write(frame); err != nil {
		return nil, fmt.Errorf("tx510: write %s: %w", cmd, err)
	}

	reply, err := ReadFrame(c.t)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("tx510: read %s reply: %w", cmd, err)
	}
	return reply, nil
}

// RecognizeOption tunes a single Recognize/Register call.
type RecognizeOption func(*recognizeConfig)

type recognizeConfig struct {
	livenessRetries int
	livenessDelay   time.Duration
}

// AllowLivenessRetries retries Recognize/Register on liveness rejects up to n extra times, pausing delay between attempts. Other result codes are not retried.
func AllowLivenessRetries(n int, delay time.Duration) RecognizeOption {
	return func(c *recognizeConfig) {
		if n < 0 {
			n = 0
		}
		c.livenessRetries = n
		c.livenessDelay = delay
	}
}

func (c *Client) Recognize(ctx context.Context, opts ...RecognizeOption) (uint16, error) {
	return c.recognizeOrRegister(ctx, CmdRecognize, opts)
}

func (c *Client) Register(ctx context.Context, opts ...RecognizeOption) (uint16, error) {
	return c.recognizeOrRegister(ctx, CmdRegister, opts)
}

func (c *Client) recognizeOrRegister(ctx context.Context, cmd CmdID, opts []RecognizeOption) (uint16, error) {
	var cfg recognizeConfig
	for _, o := range opts {
		o(&cfg)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	attempts := 1 + cfg.livenessRetries
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 && cfg.livenessDelay > 0 {
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-time.After(cfg.livenessDelay):
			}
		}

		reply, err := c.do(ctx, cmd, nil)
		if err != nil {
			return 0, err
		}
		if rerr := reply.Err(); rerr != nil {
			if IsLivenessError(rerr) && i < attempts-1 {
				lastErr = rerr
				continue
			}
			return 0, rerr
		}
		if len(reply.Payload) < 2 {
			return 0, fmt.Errorf("tx510: %s reply payload too short: %d bytes", cmd, len(reply.Payload))
		}
		return binary.BigEndian.Uint16(reply.Payload[:2]), nil
	}
	return 0, lastErr
}

func (c *Client) DeleteUser(ctx context.Context, faceID uint16) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	data := make([]byte, 2)
	binary.BigEndian.PutUint16(data, faceID)
	reply, err := c.do(ctx, CmdDeleteUser, data)
	if err != nil {
		return err
	}
	return reply.Err()
}

func (c *Client) DeleteAll(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	reply, err := c.do(ctx, CmdDeleteAll, nil)
	if err != nil {
		return err
	}
	return reply.Err()
}

func (c *Client) UserCount(ctx context.Context) ([]uint16, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	reply, err := c.do(ctx, CmdUserCount, nil)
	if err != nil {
		return nil, err
	}
	if err := reply.Err(); err != nil {
		return nil, err
	}
	if len(reply.Payload) < 2 {
		return nil, nil
	}
	count := binary.BigEndian.Uint16(reply.Payload[:2])
	expectedLen := 2 + int(count)*2
	if len(reply.Payload) < expectedLen {
		return nil, fmt.Errorf("tx510: UserCount: declared %d ids but payload has %d bytes",
			count, len(reply.Payload))
	}
	ids := make([]uint16, count)
	for i := range ids {
		ids[i] = binary.BigEndian.Uint16(reply.Payload[2+i*2 : 4+i*2])
	}
	return ids, nil
}

func (c *Client) Version(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	reply, err := c.do(ctx, CmdVersion, nil)
	if err != nil {
		return "", err
	}
	if err := reply.Err(); err != nil {
		return "", err
	}
	return strings.TrimRight(string(reply.Payload), "\x00"), nil
}

func (c *Client) Reboot(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	reply, err := c.do(ctx, CmdReboot, nil)
	if err != nil {
		return err
	}
	return reply.Err()
}

func (c *Client) SetBacklight(ctx context.Context, on bool) error {
	return c.toggle(ctx, CmdBacklight, on)
}

func (c *Client) SetDisplay(ctx context.Context, on bool) error {
	return c.toggle(ctx, CmdDisplay, on)
}

func (c *Client) SetWhiteLight(ctx context.Context, on bool) error {
	return c.toggle(ctx, CmdWhiteLight, on)
}

func (c *Client) toggle(ctx context.Context, cmd CmdID, on bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var v byte
	if on {
		v = 1
	}
	reply, err := c.do(ctx, cmd, []byte{v})
	if err != nil {
		return err
	}
	return reply.Err()
}

func (c *Client) SetBaudRate(ctx context.Context, rate BaudRate) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	reply, err := c.do(ctx, CmdBaudRate, []byte{byte(rate)})
	if err != nil {
		return err
	}
	if err := reply.Err(); err != nil {
		return err
	}
	if r, ok := c.t.(reconfigurer); ok {
		if hostBaud := rate.Int(); hostBaud > 0 {
			return r.Reconfigure(hostBaud)
		}
	}
	return nil
}

const TemplateSize = 1024
const readTemplateOneShotSeq = 0x0F
const templateRand = 0x42

func (c *Client) ReadTemplate(ctx context.Context, faceID uint16) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	data := make([]byte, 4)
	data[0] = templateRand
	binary.BigEndian.PutUint16(data[1:3], faceID)
	data[3] = readTemplateOneShotSeq

	reply, err := c.do(ctx, CmdReadEigenvalue, data)
	if err != nil {
		return nil, err
	}
	if err := reply.Err(); err != nil {
		return nil, err
	}
	const headerLen = 4
	if len(reply.Payload) < headerLen+TemplateSize {
		return nil, fmt.Errorf("tx510: ReadTemplate: payload %d bytes, want at least %d",
			len(reply.Payload), headerLen+TemplateSize)
	}
	out := make([]byte, TemplateSize)
	copy(out, reply.Payload[headerLen:headerLen+TemplateSize])
	return out, nil
}

func (c *Client) WriteTemplate(ctx context.Context, template []byte) error {
	if len(template) != TemplateSize {
		return fmt.Errorf("tx510: WriteTemplate: template is %d bytes, want %d",
			len(template), TemplateSize)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	const chunkSize = 256
	var (
		sendSeqs  = [4]byte{0x01, 0x02, 0x04, 0x08}
		expectAck = [4]byte{0x01, 0x03, 0x07, 0x0F}
	)

	for i := 0; i < 4; i++ {
		data := make([]byte, 0, 2+chunkSize)
		data = append(data, templateRand, sendSeqs[i])
		data = append(data, template[i*chunkSize:(i+1)*chunkSize]...)

		reply, err := c.do(ctx, CmdWriteEigenvalue, data)
		if err != nil {
			return fmt.Errorf("tx510: WriteTemplate chunk %d/4: %w", i+1, err)
		}
		if err := reply.Err(); err != nil {
			return fmt.Errorf("tx510: WriteTemplate chunk %d/4: %w", i+1, err)
		}

		if len(reply.Payload) < 2 {
			return fmt.Errorf("tx510: WriteTemplate chunk %d/4: ack payload too short", i+1)
		}
		gotSeq := reply.Payload[1]
		if gotSeq != expectAck[i] {
			return fmt.Errorf("tx510: WriteTemplate chunk %d/4: ack seq=0x%02x, want 0x%02x",
				i+1, gotSeq, expectAck[i])
		}
	}
	return nil
}

// DumpAll writes every enrolled template to dir as `<faceID:05d>.tpl` and returns the IDs dumped. The directory is created if missing. Fails fast on the first error.
func (c *Client) DumpAll(ctx context.Context, dir string) ([]uint16, error) {
	ids, err := c.UserCount(ctx)
	if err != nil {
		return nil, fmt.Errorf("tx510: DumpAll: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("tx510: DumpAll: %w", err)
	}
	for _, id := range ids {
		tpl, err := c.ReadTemplate(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("tx510: DumpAll: read faceID %d: %w", id, err)
		}
		path := filepath.Join(dir, fmt.Sprintf("%05d.tpl", id))
		if err := os.WriteFile(path, tpl, 0o644); err != nil {
			return nil, fmt.Errorf("tx510: DumpAll: write %s: %w", path, err)
		}
	}
	return ids, nil
}

// RestoreAll uploads every *.tpl file in dir (sorted by filename) via WriteTemplate and returns the count restored. The device assigns new faceIDs; filenames are host-side bookkeeping only.
func (c *Client) RestoreAll(ctx context.Context, dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("tx510: RestoreAll: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".tpl") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return 0, fmt.Errorf("tx510: RestoreAll: %w", err)
		}
		if err := c.WriteTemplate(ctx, data); err != nil {
			return 0, fmt.Errorf("tx510: RestoreAll: %s: %w", name, err)
		}
	}
	return len(names), nil
}
