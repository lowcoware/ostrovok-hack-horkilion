// Command inventory serves hotel search, offers and weather over gRPC.
package main

import (
	"log/slog"
	"os"

	"google.golang.org/grpc"

	inventoryv1 "horkilion/gen/inventory/v1"
	"horkilion/internal/inventory"
	"horkilion/internal/platform"
)

func main() {
	platform.HandleHealthcheck()
	platform.NewLogger("inventory")
	ctx, stop := platform.SignalContext()
	defer stop()

	platform.StartOps(ctx)
	srv := inventory.NewServer(inventory.NewWeather())
	err := platform.ServeGRPC(ctx, func(g *grpc.Server) {
		inventoryv1.RegisterInventoryServiceServer(g, srv)
	})
	if err != nil {
		slog.Error("grpc server failed", "err", err)
		os.Exit(1)
	}
}
