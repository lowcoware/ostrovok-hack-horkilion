// Command gateway serves the HTTP API and relays agent events over SSE.
package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	bookingv1 "horkilion/gen/booking/v1"
	"horkilion/internal/gateway"
	"horkilion/internal/platform"
)

func main() {
	platform.HandleHealthcheck()
	platform.NewLogger("gateway")
	if err := run(); err != nil {
		slog.Error("gateway failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := platform.SignalContext()
	defer stop()

	nc, err := platform.ConnectNATS("gateway")
	if err != nil {
		return err
	}
	defer nc.Drain()
	js, err := jetstream.New(nc)
	if err != nil {
		return err
	}
	if err := platform.EnsureStreams(ctx, js); err != nil {
		return err
	}

	bookingConn, err := platform.DialGRPC(platform.Env("BOOKING_ADDR", "booking:9000"))
	if err != nil {
		return err
	}
	defer bookingConn.Close()

	platform.StartOps(ctx, platform.NATSReady(nc))
	gw := gateway.New(nc, js, bookingv1.NewBookingServiceClient(bookingConn))
	srv := &http.Server{
		Addr:              platform.Env("HTTP_ADDR", ":8080"),
		Handler:           gw.Router(),
		ReadHeaderTimeout: 5 * time.Second,
		// No WriteTimeout: SSE responses stay open.
	}
	return platform.ServeHTTP(ctx, srv)
}
