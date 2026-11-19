// Command detect-server runs the standalone adversarial detection and
// behavioral-telemetry HTTP server. Any browser that opens its root URL runs
// the full probe suite, renders an HTML report, and posts the structured JSON
// report to /api/report (retrievable at /api/latest and /api/connection).
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

	"github.com/xtrcom/xt-nichrome/internal/detect"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "detect-server: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	listen := flag.String("listen", "127.0.0.1:7799", "listen address")
	flag.Parse()

	srv := detect.NewServer()
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		fmt.Fprintf(os.Stderr, "detect-server listening on http://%s\n", ln.Addr().String())
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return httpSrv.Shutdown(sctx)
}
