// Package tracing exports OpenTelemetry traces to Langfuse over OTLP/HTTP.
// Langfuse reads the langfuse.* and gen_ai.* span attributes to build traces,
// generations and sessions.
package tracing

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Attribute keys understood by Langfuse.
const (
	TraceName       = attribute.Key("langfuse.trace.name")
	TraceTags       = attribute.Key("langfuse.trace.tags")
	SessionID       = attribute.Key("langfuse.session.id")
	ObservationType = attribute.Key("langfuse.observation.type")
	Input           = attribute.Key("langfuse.observation.input")
	Output          = attribute.Key("langfuse.observation.output")
	Model           = attribute.Key("gen_ai.request.model")
	InputTokens     = attribute.Key("gen_ai.usage.input_tokens")
	OutputTokens    = attribute.Key("gen_ai.usage.output_tokens")
)

type Config struct {
	Service   string
	Host      string
	PublicKey string
	SecretKey string
}

// Setup installs the global tracer provider. Without keys tracing stays a no-op
// and the returned shutdown does nothing.
func Setup(ctx context.Context, cfg Config) (shutdown func(context.Context) error, err error) {
	if cfg.PublicKey == "" || cfg.SecretKey == "" {
		slog.Info("langfuse keys are not set, tracing is disabled")
		return func(context.Context) error { return nil }, nil
	}
	auth := base64.StdEncoding.EncodeToString([]byte(cfg.PublicKey + ":" + cfg.SecretKey))
	exp, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(strings.TrimRight(cfg.Host, "/")+"/api/public/otel/v1/traces"),
		otlptracehttp.WithHeaders(map[string]string{
			"Authorization": "Basic " + auth,
			// Makes new data appear in Langfuse in real time.
			"x-langfuse-ingestion-version": "4",
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("create otlp exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", cfg.Service))),
	)
	otel.SetTracerProvider(tp)
	slog.Info("tracing to langfuse", "host", cfg.Host)
	return tp.Shutdown, nil
}

// JSON renders v for the input and output attributes.
func JSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
