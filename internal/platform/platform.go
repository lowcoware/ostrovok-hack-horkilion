// Package platform holds the plumbing every service shares: configuration,
// logging, the ops HTTP server (health and metrics), and connections to NATS
// and PostgreSQL.
package platform

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Env returns the value of key or def when it is unset or empty.
func Env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// EnvInt returns key parsed as an integer, or def when it is unset or invalid.
func EnvInt(key string, def int) int {
	v, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return def
	}
	return v
}

// MustEnv returns the value of key and exits when it is missing.
func MustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		slog.Error("required environment variable is missing", "key", key)
		os.Exit(1)
	}
	return v
}

// NewLogger installs a JSON logger tagged with the service name as the default.
func NewLogger(service string) *slog.Logger {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", service)
	slog.SetDefault(log)
	return log
}

// HandleHealthcheck implements `<binary> healthcheck` for container health
// checks: the runtime image has no curl or wget. It exits the process when the
// subcommand is given and returns otherwise.
func HandleHealthcheck() {
	if len(os.Args) < 2 || os.Args[1] != "healthcheck" {
		return
	}
	addr := Env("OPS_ADDR", ":8081")
	if strings.HasPrefix(addr, ":") {
		addr = "localhost" + addr
	}
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + addr + "/healthz")
	if err != nil || resp.StatusCode != http.StatusOK {
		os.Exit(1)
	}
	os.Exit(0)
}

// SignalContext is cancelled on SIGINT or SIGTERM.
func SignalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// ReadyCheck reports whether a dependency is usable.
type ReadyCheck func(ctx context.Context) error

// OpsHandler serves /healthz (process is alive), /readyz (dependencies are
// reachable) and /metrics (Prometheus).
func OpsHandler(checks ...ReadyCheck) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		for _, check := range checks {
			if err := check(ctx); err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("/metrics", promhttp.Handler())
	return mux
}

// ServeHTTP runs srv until ctx is cancelled, then shuts it down gracefully.
func ServeHTTP(ctx context.Context, srv *http.Server) error {
	errc := make(chan error, 1)
	go func() {
		slog.Info("http server listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
		close(errc)
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// StartOps runs the ops server on OPS_ADDR in the background.
func StartOps(ctx context.Context, checks ...ReadyCheck) {
	srv := &http.Server{
		Addr:              Env("OPS_ADDR", ":8081"),
		Handler:           OpsHandler(checks...),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := ServeHTTP(ctx, srv); err != nil {
			slog.Error("ops server failed", "err", err)
		}
	}()
}

// ConnectNATS connects to NATS_URL, waits up to 30 seconds for the first
// connection, and keeps reconnecting forever after that. Waiting here means
// start order does not matter and NATS needs no container health check.
func ConnectNATS(name string) (*nats.Conn, error) {
	nc, err := nats.Connect(
		Env("NATS_URL", nats.DefaultURL),
		nats.Name(name),
		nats.MaxReconnects(-1),
		nats.RetryOnFailedConnect(true),
		nats.ReconnectWait(time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("connect nats: %w", err)
	}
	for deadline := time.Now().Add(30 * time.Second); !nc.IsConnected(); {
		if time.Now().After(deadline) {
			nc.Close()
			return nil, errors.New("connect nats: not connected after 30s")
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nc, nil
}

// NATSReady fails while the connection is not established.
func NATSReady(nc *nats.Conn) ReadyCheck {
	return func(context.Context) error {
		if !nc.IsConnected() {
			return errors.New("nats is not connected")
		}
		return nil
	}
}

// ConnectPostgres opens a pool to DATABASE_URL.
func ConnectPostgres(ctx context.Context) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, MustEnv("DATABASE_URL"))
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	return pool, nil
}

// PostgresReady pings the pool.
func PostgresReady(pool *pgxpool.Pool) ReadyCheck {
	return func(ctx context.Context) error { return pool.Ping(ctx) }
}
