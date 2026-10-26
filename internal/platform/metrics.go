package platform

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

// Request metrics shared by every service, served on /metrics of the ops
// server. Labels stay low-cardinality: the route template, not the raw path.
var (
	httpRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "HTTP requests by route, method and status code.",
	}, []string{"route", "method", "code"})
	httpDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request duration by route and method. Streams (SSE) last as long as the client stays connected.",
		Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
	}, []string{"route", "method"})

	grpcHandled = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "grpc_server_handled_total",
		Help: "gRPC calls handled by the server, by service, method and status code.",
	}, []string{"grpc_service", "grpc_method", "grpc_code"})
	grpcDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "grpc_server_handling_seconds",
		Help:    "gRPC call duration on the server, by service and method.",
		Buckets: []float64{0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
	}, []string{"grpc_service", "grpc_method"})
)

// HTTPMetrics records every request of a gin engine. Requests that match no
// route are counted under "unmatched", so scanners cannot inflate the labels.
func HTTPMetrics(c *gin.Context) {
	start := time.Now()
	c.Next()
	route := c.FullPath()
	if route == "" {
		route = "unmatched"
	}
	httpRequests.WithLabelValues(route, c.Request.Method, strconv.Itoa(c.Writer.Status())).Inc()
	httpDuration.WithLabelValues(route, c.Request.Method).Observe(time.Since(start).Seconds())
}

// grpcMetrics records unary calls of a gRPC server.
func grpcMetrics(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	start := time.Now()
	resp, err := handler(ctx, req)
	service, method := splitMethod(info.FullMethod)
	grpcHandled.WithLabelValues(service, method, status.Code(err).String()).Inc()
	grpcDuration.WithLabelValues(service, method).Observe(time.Since(start).Seconds())
	return resp, err
}

// splitMethod turns "/booking.v1.BookingService/CreateOrder" into its service
// and method.
func splitMethod(full string) (service, method string) {
	full = strings.TrimPrefix(full, "/")
	if i := strings.LastIndex(full, "/"); i >= 0 {
		return full[:i], full[i+1:]
	}
	return "unknown", full
}
