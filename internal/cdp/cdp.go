// Package cdp implements a minimal, dependency-free Chrome DevTools Protocol
// transport over --remote-debugging-pipe.
//
// Why a pipe instead of a WebSocket: no listening debug port exists, so the
// public surface of the daemon is the only reachable interface (brief §6:
// "leave browser debugging interfaces private").
package cdp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// ErrClosed is returned once the browser pipe is gone.
var ErrClosed = errors.New("cdp: browser connection closed")

// ProtocolError is a structured CDP error response.
type ProtocolError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

func (e *ProtocolError) Error() string {
	if e.Data != "" {
		return fmt.Sprintf("cdp %d: %s (%s)", e.Code, e.Message, e.Data)
	}
	return fmt.Sprintf("cdp %d: %s", e.Code, e.Message)
}

type rawMessage struct {
	ID        int64           `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *ProtocolError  `json:"error,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
}

type response struct {
	result json.RawMessage
	err    error
}

// EventHandler receives browser events. It must not block.
type EventHandler func(method, sessionID string, params json.RawMessage)

// Conn is a single browser process connection.
type Conn struct {
	cmd *exec.Cmd
	w   *os.File
	r   *bufio.Reader

	writeMu sync.Mutex
	nextID  atomic.Int64

	mu      sync.Mutex
	pending map[int64]chan response
	handler EventHandler
	closed  bool
	err     error
	done    chan struct{}
	stderr  *ringBuffer
	exited  chan struct{}
}

// Options describes how to launch the browser process.
type Options struct {
	ExecPath string
	Args     []string
	Env      []string
	Stderr   io.Writer
}

// Launch starts a browser with --remote-debugging-pipe and returns a Conn.
func Launch(opts Options) (*Conn, error) {
	args := append([]string{}, opts.Args...)
	args = append(args, "--remote-debugging-pipe")

	cmd := exec.Command(opts.ExecPath, args...)
	if opts.Env != nil {
		cmd.Env = opts.Env
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("cdp: pipe: %w", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, fmt.Errorf("cdp: pipe: %w", err)
	}
	// fd 3 = browser reads commands, fd 4 = browser writes responses.
	cmd.ExtraFiles = []*os.File{inR, outW}
	stderr := newRingBuffer(64 << 10)
	cmd.Stderr = stderr
	cmd.Stdout = io.Discard

	if err := cmd.Start(); err != nil {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		return nil, fmt.Errorf("cdp: start browser: %w", err)
	}
	// Parent must drop the child ends or reads never see EOF.
	inR.Close()
	outW.Close()

	c := &Conn{
		cmd:     cmd,
		w:       inW,
		r:       bufio.NewReaderSize(outR, 1<<20),
		pending: make(map[int64]chan response),
		done:    make(chan struct{}),
		stderr:  stderr,
		exited:  make(chan struct{}),
	}
	go c.readLoop(outR)
	go func() {
		cmd.Wait()
		close(c.exited)
		c.fail(ErrClosed)
	}()
	return c, nil
}

// SetEventHandler installs the event sink. Install before issuing commands.
func (c *Conn) SetEventHandler(h EventHandler) {
	c.mu.Lock()
	c.handler = h
	c.mu.Unlock()
}

// PID reports the browser process id.
func (c *Conn) PID() int {
	if c.cmd == nil || c.cmd.Process == nil {
		return 0
	}
	return c.cmd.Process.Pid
}

// Stderr returns captured browser stderr (bounded).
func (c *Conn) Stderr() string { return c.stderr.String() }

// Exited is closed when the browser process has exited.
func (c *Conn) Exited() <-chan struct{} { return c.exited }

// Done is closed when the connection is unusable.
func (c *Conn) Done() <-chan struct{} { return c.done }

func (c *Conn) fail(err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.err = err
	pending := c.pending
	c.pending = map[int64]chan response{}
	c.mu.Unlock()
	close(c.done)
	for _, ch := range pending {
		select {
		case ch <- response{err: err}:
		default:
		}
	}
}

func (c *Conn) readLoop(f *os.File) {
	defer f.Close()
	for {
		line, err := c.r.ReadBytes(0)
		if err != nil {
			c.fail(ErrClosed)
			return
		}
		if len(line) < 2 {
			continue
		}
		var m rawMessage
		if err := json.Unmarshal(line[:len(line)-1], &m); err != nil {
			continue
		}
		if m.ID != 0 && m.Method == "" {
			c.mu.Lock()
			ch := c.pending[m.ID]
			delete(c.pending, m.ID)
			c.mu.Unlock()
			if ch != nil {
				if m.Error != nil {
					ch <- response{err: m.Error}
				} else {
					ch <- response{result: m.Result}
				}
			}
			continue
		}
		if m.Method != "" {
			c.mu.Lock()
			h := c.handler
			c.mu.Unlock()
			if h != nil {
				h(m.Method, m.SessionID, m.Params)
			}
		}
	}
}

// Call performs one CDP command. out may be nil; otherwise it is decoded from
// the result payload.
func (c *Conn) Call(ctx context.Context, sessionID, method string, params any, out any) error {
	if params == nil {
		params = struct{}{}
	}
	id := c.nextID.Add(1)
	payload := map[string]any{"id": id, "method": method, "params": params}
	if sessionID != "" {
		payload["sessionId"] = sessionID
	}
	bytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("cdp: encode %s: %w", method, err)
	}
	ch := make(chan response, 1)
	c.mu.Lock()
	if c.closed {
		err := c.err
		c.mu.Unlock()
		if err == nil {
			err = ErrClosed
		}
		return err
	}
	c.pending[id] = ch
	c.mu.Unlock()

	c.writeMu.Lock()
	bytes = append(bytes, 0)
	_, werr := c.w.Write(bytes)
	c.writeMu.Unlock()
	if werr != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("cdp: write %s: %w", method, werr)
	}

	select {
	case res := <-ch:
		if res.err != nil {
			return res.err
		}
		if out != nil && len(res.result) > 0 {
			if err := json.Unmarshal(res.result, out); err != nil {
				return fmt.Errorf("cdp: decode %s result: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	case <-c.done:
		return ErrClosed
	}
}

// Close kills the browser process tree and waits briefly for exit.
func (c *Conn) Close() error {
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Signal(os.Interrupt)
		select {
		case <-c.exited:
		case <-time.After(3 * time.Second):
			_ = c.cmd.Process.Kill()
			select {
			case <-c.exited:
			case <-time.After(3 * time.Second):
			}
		}
	}
	c.writeMu.Lock()
	_ = c.w.Close()
	c.writeMu.Unlock()
	c.fail(ErrClosed)
	return nil
}

type ringBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func newRingBuffer(max int) *ringBuffer { return &ringBuffer{max: max} }

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.max {
		r.buf = r.buf[len(r.buf)-r.max:]
	}
	return len(p), nil
}

func (r *ringBuffer) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}
