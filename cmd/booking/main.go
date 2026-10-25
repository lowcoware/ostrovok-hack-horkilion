// Command booking serves orders over gRPC and relays booking events to JetStream.
package main

import (
	"log/slog"
	"os"

	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/grpc"

	bookingv1 "horkilion/gen/booking/v1"
	lodgingv1 "horkilion/gen/lodging/v1"
	transferv1 "horkilion/gen/transfer/v1"
	transportv1 "horkilion/gen/transport/v1"
	"horkilion/internal/booking"
	"horkilion/internal/platform"
)

func main() {
	platform.HandleHealthcheck()
	platform.NewLogger("booking")
	if err := run(); err != nil {
		slog.Error("booking failed", "err", err)
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

	nc, err := platform.ConnectNATS("booking")
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

	invConn, err := platform.DialGRPC(platform.Env("INVENTORY_ADDR", "inventory:9000"))
	if err != nil {
		return err
	}
	defer invConn.Close()

	platform.StartOps(ctx, platform.PostgresReady(db), platform.NATSReady(nc))
	go booking.RunOutboxRelay(ctx, db, js)

	srv := booking.NewServer(db, booking.Suppliers{
		Lodging:   lodgingv1.NewLodgingServiceClient(invConn),
		Transport: transportv1.NewTransportServiceClient(invConn),
		Transfer:  transferv1.NewTransferServiceClient(invConn),
	})
	return platform.ServeGRPC(ctx, func(g *grpc.Server) {
		bookingv1.RegisterBookingServiceServer(g, srv)
	})
}
