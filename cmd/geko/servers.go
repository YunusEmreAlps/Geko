package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/YunusEmreAlps/geko/internal/access"
	"github.com/YunusEmreAlps/geko/internal/ingest/otlp"
	"google.golang.org/grpc"
)

type httpEndpoint struct {
	listener net.Listener
	server   *http.Server
}

func newHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
			handler.ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 20 * time.Second, IdleTimeout: 30 * time.Second,
	}
}

// All listeners are bound before startup. An unexpected serve failure stops
// every transport; the database is closed only after their handlers finish.
func runServers(ctx context.Context, out io.Writer, endpoints []httpEndpoint, grpcListener net.Listener, receiver *otlp.Receiver, token access.Token) error {
	count := len(endpoints)
	if grpcListener != nil {
		count++
	}
	done := make(chan error, count)
	for _, endpoint := range endpoints {
		go func() {
			err := endpoint.server.Serve(endpoint.listener)
			if errors.Is(err, http.ErrServerClosed) {
				err = nil
			}
			done <- err
		}()
	}
	grpcServer := receiver.NewGRPCServer(token)
	if grpcListener != nil {
		go func() {
			err := grpcServer.Serve(grpcListener)
			if errors.Is(err, grpc.ErrServerStopped) {
				err = nil
			}
			done <- err
		}()
	}
	fmt.Fprintf(out, "Geko listening on http://%s\n", endpoints[0].listener.Addr())
	if len(endpoints) > 1 {
		fmt.Fprintf(out, "Dashboard listening on http://%s\n", endpoints[1].listener.Addr())
	}
	if grpcListener != nil {
		fmt.Fprintf(out, "OTLP/gRPC listening on %s\n", grpcListener.Addr())
	}
	if names := token.Names(access.ScopeIngest); len(names) > 0 {
		fmt.Fprintf(out, "Ingest tokens: %s\n", strings.Join(names, ", "))
	}
	listeners := []net.Listener{grpcListener}
	for _, endpoint := range endpoints {
		listeners = append(listeners, endpoint.listener)
	}
	for _, listener := range listeners {
		if listener != nil && !loopbackListener(listener) {
			fmt.Fprintf(out, "Warning: %s is not a loopback address and serves unencrypted HTTP. "+
				"Expose it only on a loopback or trusted interface, or behind a TLS reverse proxy; "+
				"the access token and telemetry are sent in cleartext.\n", listener.Addr())
		}
	}
	var result error
	select {
	case result = <-done:
		count--
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stopped := make(chan error, len(endpoints)+1)
	for _, endpoint := range endpoints {
		go func() {
			err := endpoint.server.Shutdown(shutdown)
			if err != nil {
				err = errors.Join(err, endpoint.server.Close())
			}
			stopped <- err
		}()
	}
	go func() {
		graceful := make(chan struct{})
		go func() { grpcServer.GracefulStop(); close(graceful) }()
		select {
		case <-graceful:
		case <-shutdown.Done():
			grpcServer.Stop()
			<-graceful
		}
		stopped <- nil
	}()
	for range len(endpoints) + 1 {
		result = errors.Join(result, <-stopped)
	}
	for range count {
		result = errors.Join(result, <-done)
	}
	return result
}

func loopbackListener(listener net.Listener) bool {
	address, err := netip.ParseAddrPort(listener.Addr().String())
	return err == nil && address.Addr().Unmap().IsLoopback()
}
