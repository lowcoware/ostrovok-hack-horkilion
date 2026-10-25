// Command inventory hosts the supplier services over gRPC: catalog
// (destinations, weather), lodging (hotels), transport (flights, trains) and
// transfer. Each service runs the provider named in its *_PROVIDER variable.
package main

import (
	"fmt"
	"log/slog"
	"os"

	"google.golang.org/grpc"

	catalogv1 "horkilion/gen/catalog/v1"
	lodgingv1 "horkilion/gen/lodging/v1"
	transferv1 "horkilion/gen/transfer/v1"
	transportv1 "horkilion/gen/transport/v1"
	"horkilion/internal/catalog"
	"horkilion/internal/dataset"
	"horkilion/internal/lodging"
	"horkilion/internal/platform"
	"horkilion/internal/transfer"
	"horkilion/internal/transport"
)

func main() {
	platform.HandleHealthcheck()
	platform.NewLogger("inventory")
	if err := run(); err != nil {
		slog.Error("inventory failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := platform.SignalContext()
	defer stop()

	dataDir := platform.Env("DATA_DIR", "/data")
	data, err := dataset.Load(dataDir)
	if err != nil {
		return fmt.Errorf("load data snapshot from %s: %w", dataDir, err)
	}
	slog.Info("data snapshot loaded", "dir", dataDir, "built_at", data.Manifest.BuiltAt,
		"destinations", len(data.Destinations), "hotels", len(data.Hotels))

	catalogSrv, err := platform.Provider("CATALOG_PROVIDER", "open-data", map[string]func() catalogv1.CatalogServiceServer{
		"open-data": func() catalogv1.CatalogServiceServer { return catalog.NewOpenData(data) },
	})
	if err != nil {
		return err
	}
	lodgingSrv, err := platform.Provider("LODGING_PROVIDER", "osm", map[string]func() lodgingv1.LodgingServiceServer{
		"osm":       func() lodgingv1.LodgingServiceServer { return lodging.NewOSM(data) },
		"synthetic": func() lodgingv1.LodgingServiceServer { return lodging.Synthetic{} },
	})
	if err != nil {
		return err
	}
	transportSrv, err := platform.Provider("TRANSPORT_PROVIDER", "stub", map[string]func() transportv1.TransportServiceServer{
		"stub": func() transportv1.TransportServiceServer { return transport.Stub{} },
	})
	if err != nil {
		return err
	}
	transferSrv, err := platform.Provider("TRANSFER_PROVIDER", "stub", map[string]func() transferv1.TransferServiceServer{
		"stub": func() transferv1.TransferServiceServer { return transfer.Stub{} },
	})
	if err != nil {
		return err
	}

	platform.StartOps(ctx)
	// impulse: one process hosts all four supplier services, split them into separate deployments when one provider needs its own scaling or release cycle.
	return platform.ServeGRPC(ctx, func(g *grpc.Server) {
		catalogv1.RegisterCatalogServiceServer(g, catalogSrv)
		lodgingv1.RegisterLodgingServiceServer(g, lodgingSrv)
		transportv1.RegisterTransportServiceServer(g, transportSrv)
		transferv1.RegisterTransferServiceServer(g, transferSrv)
	})
}
