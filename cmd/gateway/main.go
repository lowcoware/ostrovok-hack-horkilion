// Command gateway serves the HTTP API and relays agent events over SSE.
package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	bookingv1 "horkilion/gen/booking/v1"
	catalogv1 "horkilion/gen/catalog/v1"
	"horkilion/internal/dataset"
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

	db, err := platform.ConnectPostgres(ctx)
	if err != nil {
		return err
	}
	defer db.Close()

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

	invConn, err := platform.DialGRPC(platform.Env("INVENTORY_ADDR", "inventory:9000"))
	if err != nil {
		return err
	}
	defer invConn.Close()
	// The "Откуда данные" page comes from the registry of the same snapshot inventory serves.
	// impulse: the gateway loads the whole snapshot to validate the registry, read only manifest.json and sources.yaml when the snapshot outgrows the gateway's memory.
	data, err := dataset.Load(platform.Env("DATA_DIR", "/data"))
	if err != nil {
		return fmt.Errorf("load data snapshot: %w", err)
	}

	platform.StartOps(ctx, platform.PostgresReady(db), platform.NATSReady(nc))
	gw := gateway.New(gateway.NewPGStore(db), gateway.NATSTurns{JS: js}, gateway.NATSEvents{NC: nc}, gateway.Deps{
		Booking: bookingv1.NewBookingServiceClient(bookingConn),
		Catalog: catalogv1.NewCatalogServiceClient(invConn),
		Sources: gateway.Providers(data.Sources),
	})
	srv := &http.Server{
		Addr:              platform.Env("HTTP_ADDR", ":8080"),
		Handler:           gw.Router(),
		ReadHeaderTimeout: 5 * time.Second,
		// No WriteTimeout: SSE responses stay open.
	}
	return platform.ServeHTTP(ctx, srv)
}
