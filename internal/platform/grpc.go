package platform

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// ServeGRPC runs a gRPC server with the standard health service on GRPC_ADDR
// until ctx is cancelled, then stops it gracefully.
func ServeGRPC(ctx context.Context, register func(*grpc.Server)) error {
	addr := Env("GRPC_ADDR", ":9000")
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	srv := grpc.NewServer(grpc.ChainUnaryInterceptor(grpcMetrics))
	register(srv)
	healthpb.RegisterHealthServer(srv, health.NewServer())

	errc := make(chan error, 1)
	go func() {
		slog.Info("grpc server listening", "addr", addr)
		errc <- srv.Serve(lis)
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		srv.GracefulStop()
		return nil
	}
}

// DialGRPC creates a client connection to target. Traffic stays inside the
// compose network.
// impulse: plaintext gRPC inside the docker network, add mTLS when services run on more than one host.
func DialGRPC(target string) (*grpc.ClientConn, error) {
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", target, err)
	}
	return conn, nil
}
