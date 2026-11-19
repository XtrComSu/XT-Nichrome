// Command fixture-server serves the deterministic test pages used by the
// end-to-end suite and by manual exploration.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/xtrcom/xt-nichrome/internal/fixtures"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fixture-server: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("listen", "127.0.0.1:7788", "listen address")
	denied := flag.String("denied", "", "denied target base URL (default http://denied.invalid)")
	flag.Parse()

	s := fixtures.New()
	defer s.Close()
	if *denied != "" {
		s.SetDeniedURL(*denied)
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		fmt.Fprintf(os.Stderr, "fixture-server listening on http://%s\n", ln.Addr().String())
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}
