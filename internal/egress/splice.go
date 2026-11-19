package egress

import (
	"context"
	"io"
	"sync/atomic"
	"time"
)

// contextWithTimeout is a tiny helper so resolution shares the dial budget.
func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// countingWriter adds every write to an atomic byte counter.
type countingWriter struct {
	w       io.Writer
	counter *atomic.Int64
}

func (c *countingWriter) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	if n > 0 {
		c.counter.Add(int64(n))
	}
	return n, err
}

// countingReadCloser adds every read to an atomic byte counter.
type countingReadCloser struct {
	r       io.ReadCloser
	counter *atomic.Int64
}

func (c *countingReadCloser) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	if n > 0 {
		c.counter.Add(int64(n))
	}
	return n, err
}

func (c *countingReadCloser) Close() error { return c.r.Close() }

// halfCloser is satisfied by *net.TCPConn and similar.
type halfCloser interface {
	CloseWrite() error
}

// closeWrite half-closes the write side when the transport supports it, so the
// peer observes EOF instead of a reset.
func closeWrite(c io.ReadWriteCloser) {
	if hc, ok := c.(halfCloser); ok {
		_ = hc.CloseWrite()
		return
	}
	_ = c.Close()
}

// splice copies bytes in both directions between the client side and the origin
// side of a tunnel, counting bytes and half-closing each direction independently
// so a one-sided shutdown does not truncate the other.
//
// The read side of each direction is passed separately from the conn because a
// hijacked net/http conn may already hold bytes in a bufio.Reader: those bytes
// belong to the tunnel and must be consumed in the right direction. clientRead
// feeds the client→origin copy, originRead feeds the origin→client copy.
//
// Both loops exit when either conn is closed — Close() terminates live tunnels by
// closing the conns in the proxy's registry — so this always returns and never
// leaks the two goroutines.
func (p *Proxy) splice(client io.ReadWriteCloser, clientRead io.Reader, origin io.ReadWriteCloser, originRead io.Reader) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(&countingWriter{w: origin, counter: &p.bytesUp}, clientRead)
		closeWrite(origin)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(&countingWriter{w: client, counter: &p.bytesDown}, originRead)
		closeWrite(client)
		done <- struct{}{}
	}()
	<-done
	<-done
	_ = origin.Close()
	_ = client.Close()
}
