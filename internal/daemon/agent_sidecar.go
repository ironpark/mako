package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ironpark/mako/internal/agent/agy"
)

const sidecarTokenFile = "sidecar.token"

var errSidecarUnauthorized = errors.New("unauthorized sidecar subscription")

type sidecarPush struct {
	ID             uint64 `json:"id"`
	SessionID      string `json:"session_id"`
	ConversationID string `json:"conversation_id"`
	Title          string `json:"title"`
	Text           string `json:"text"`
	Home           string `json:"home,omitempty"`
}

type sidecarAck struct {
	ID    uint64 `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type sidecarFrame struct {
	sidecarPush
	Error string `json:"error,omitempty"`
}

type sidecarDeliverer interface {
	DeliverMessage(ctx context.Context, conversationID, title, text string, env map[string]string) error
}

type agySidecarClient struct {
	conn   net.Conn
	enc    *json.Encoder
	dec    *json.Decoder
	mu     chan struct{} // 1-buffered lock for write/delivery serialization
	closed chan struct{} // closed once reader loop finishes
	ackMu  sync.Mutex
	acks   map[uint64]chan sidecarAck
}

func (c *agySidecarClient) deliver(ctx context.Context, push sidecarPush) error {
	select {
	case c.mu <- struct{}{}:
		defer func() { <-c.mu }()
	case <-c.closed:
		return errors.New("sidecar disconnected")
	case <-ctx.Done():
		return ctx.Err()
	}

	select {
	case <-c.closed:
		return errors.New("sidecar disconnected")
	default:
	}

	ackCh := make(chan sidecarAck, 1)
	c.ackMu.Lock()
	if c.acks == nil {
		c.acks = map[uint64]chan sidecarAck{}
	}
	c.acks[push.ID] = ackCh
	c.ackMu.Unlock()
	defer func() {
		c.ackMu.Lock()
		delete(c.acks, push.ID)
		c.ackMu.Unlock()
	}()

	deadline := time.Now().Add(12 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = c.conn.SetWriteDeadline(deadline)
	if err := c.enc.Encode(push); err != nil {
		_ = c.conn.Close()
		return err
	}
	_ = c.conn.SetWriteDeadline(time.Time{})

	wait := time.Until(deadline)
	if wait <= 0 {
		wait = time.Millisecond
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case ack := <-ackCh:
		if !ack.OK {
			if ack.Error != "" {
				return errors.New(ack.Error)
			}
			return errors.New("sidecar delivery failed")
		}
		return nil
	case <-c.closed:
		return errors.New("sidecar disconnected")
	case <-ctx.Done():
		_ = c.conn.Close()
		return ctx.Err()
	case <-timer.C:
		_ = c.conn.Close()
		return errors.New("sidecar delivery timed out")
	}
}

func (d *Daemon) serveAgySidecar(ctx context.Context, conn net.Conn) {
	client := &agySidecarClient{
		conn:   conn,
		enc:    json.NewEncoder(conn),
		dec:    json.NewDecoder(bufio.NewReader(conn)),
		mu:     make(chan struct{}, 1),
		closed: make(chan struct{}),
		acks:   map[uint64]chan sidecarAck{},
	}
	d.projectAgents.mu.Lock()
	if old := d.projectAgents.agySidecar; old != nil {
		_ = old.conn.Close()
	}
	d.projectAgents.agySidecar = client
	d.projectAgents.mu.Unlock()

	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	defer func() {
		close(client.closed)
		client.ackMu.Lock()
		clear(client.acks)
		client.ackMu.Unlock()
		d.projectAgents.mu.Lock()
		if d.projectAgents.agySidecar == client {
			d.projectAgents.agySidecar = nil
		}
		d.projectAgents.mu.Unlock()
	}()

	// Sole reader loop on conn: dispatches acks to deliver() and tears down
	// immediately on EOF or connection error without waiting for ctx.Done().
	for {
		var ack sidecarAck
		if err := client.dec.Decode(&ack); err != nil {
			return
		}
		client.ackMu.Lock()
		ch := client.acks[ack.ID]
		if ch != nil {
			delete(client.acks, ack.ID)
		}
		client.ackMu.Unlock()
		if ch != nil {
			select {
			case ch <- ack:
			default:
			}
		}
	}
}

func (d *Daemon) deliverViaAgySidecar(ctx context.Context, push sidecarPush) bool {
	d.projectAgents.mu.Lock()
	client := d.projectAgents.agySidecar
	d.projectAgents.mu.Unlock()
	if client == nil {
		return false
	}
	if err := client.deliver(ctx, push); err != nil {
		d.log.Warn("agy sidecar delivery failed; falling back", "session", push.SessionID, "err", err)
		d.projectAgents.mu.Lock()
		if d.projectAgents.agySidecar == client {
			d.projectAgents.agySidecar = nil
		}
		d.projectAgents.mu.Unlock()
		_ = client.conn.Close()
		return false
	}
	return true
}

type sidecarRetryPolicy struct {
	initial    time.Duration
	max        time.Duration
	maxMissing time.Duration
}

// runAgySidecar runs inside Antigravity's sidecar supervisor, bridging Mako
// messages from the daemon's CLI socket into agy conversations via agentapi.
func runAgySidecar(args []string, stderr io.Writer) error {
	if len(args) != 1 || args[0] == "" {
		return errors.New("expected agent CLI socket path")
	}
	return runAgySidecarLoop(context.Background(), args[0], agy.New(), stderr, sidecarRetryPolicy{
		initial:    2 * time.Second,
		max:        30 * time.Second,
		maxMissing: 60 * time.Second,
	})
}

func runAgySidecarLoop(ctx context.Context, socket string, driver sidecarDeliverer, stderr io.Writer, policy sidecarRetryPolicy) error {
	if policy.initial <= 0 {
		policy.initial = 2 * time.Second
	}
	if policy.max < policy.initial {
		policy.max = policy.initial
	}
	backoff := policy.initial
	var missingSince time.Time

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		progressed, err := runAgySidecarOnce(socket, driver)
		if errors.Is(err, errSidecarUnauthorized) {
			fmt.Fprintln(stderr, "mako agy-sidecar:", err)
			return err
		}
		if progressed {
			backoff = policy.initial
			missingSince = time.Time{}
		} else if err != nil {
			fmt.Fprintln(stderr, "mako agy-sidecar:", err)
			if isSidecarSocketMissing(socket, err) {
				if missingSince.IsZero() {
					missingSince = time.Now()
				}
				if policy.maxMissing > 0 && time.Since(missingSince) >= policy.maxMissing {
					return fmt.Errorf("sidecar socket unavailable for %s: %w", policy.maxMissing, err)
				}
			} else {
				missingSince = time.Time{}
			}
		}

		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if !progressed {
			backoff = min(backoff*2, policy.max)
		}
	}
}

func isSidecarSocketMissing(socket string, err error) bool {
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOENT) {
		return true
	}
	_, statErr := os.Stat(socket)
	return errors.Is(statErr, os.ErrNotExist)
}

func runAgySidecarOnce(socket string, driver sidecarDeliverer) (bool, error) {
	tokenPath := filepath.Join(filepath.Dir(socket), sidecarTokenFile)
	tokenBytes, err := os.ReadFile(tokenPath)
	if errors.Is(err, os.ErrPermission) {
		// In the workspace sandbox, which keeps the token from it: the
		// daemon types the session's messages into its terminal.
		return false, fmt.Errorf("%w: read sidecar token: %w", errSidecarUnauthorized, err)
	}
	if err != nil {
		return false, fmt.Errorf("read sidecar token: %w", err)
	}
	conn, err := net.DialTimeout("unix", socket, 3*time.Second)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	enc := json.NewEncoder(conn)
	if err := enc.Encode(agentCLIRequest{Op: "sidecar_subscribe", Token: strings.TrimSpace(string(tokenBytes))}); err != nil {
		return false, err
	}
	dec := json.NewDecoder(bufio.NewReader(conn))
	progressed := false
	for {
		var frame sidecarFrame
		if err := dec.Decode(&frame); err != nil {
			return progressed, err
		}
		if frame.Error != "" && frame.ID == 0 && frame.ConversationID == "" {
			return false, fmt.Errorf("%w: %s", errSidecarUnauthorized, frame.Error)
		}
		progressed = true
		var env map[string]string
		if frame.Home != "" {
			env = map[string]string{"HOME": frame.Home}
		}
		err := driver.DeliverMessage(context.Background(), frame.ConversationID, frame.Title, frame.Text, env)
		ack := sidecarAck{ID: frame.ID, OK: err == nil}
		if err != nil {
			ack.Error = err.Error()
		}
		if err := enc.Encode(ack); err != nil {
			return progressed, err
		}
	}
}
