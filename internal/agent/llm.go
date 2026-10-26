package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"horkilion/internal/platform"
	"horkilion/internal/tracing"
)

// LLM talks to any OpenAI-compatible endpoint (OpenRouter by default, a
// self-hosted vLLM, a corporate gateway) through the official client. A nil *LLM means no API key is
// configured and the agent runs on rules and templates.
type LLM struct {
	client      openai.Client
	model       string
	routerModel string
}

type LLMConfig struct {
	APIKey      string
	BaseURL     string
	Model       string
	RouterModel string
	// ExtraBody is merged into every request body, e.g. OpenRouter provider routing.
	ExtraBody map[string]any
}

const defaultExtraBody = `{"provider":{"data_collection":"deny"}}`

// LLM metrics for Grafana; the same calls are traced in Langfuse with their
// prompts. call is the kind of request: router, tools or reply.
var (
	llmRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_requests_total",
		Help: "LLM requests by call, model and result.",
	}, []string{"call", "model", "result"})
	llmDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "llm_request_duration_seconds",
		Help:    "LLM request duration by call and model, until the last streamed token.",
		Buckets: prometheus.ExponentialBuckets(0.25, 2, 9),
	}, []string{"call", "model"})
	llmTokens = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_tokens_total",
		Help: "LLM tokens by model and type (input, output).",
	}, []string{"model", "type"})
)

// observeLLM records one finished request. Use it deferred with the start time.
func observeLLM(call, model string, start time.Time, err error) {
	result := "ok"
	if err != nil {
		result = "error"
	}
	llmRequests.WithLabelValues(call, model, result).Inc()
	llmDuration.WithLabelValues(call, model).Observe(time.Since(start).Seconds())
}

func countTokens(model string, input, output int64) {
	llmTokens.WithLabelValues(model, "input").Add(float64(input))
	llmTokens.WithLabelValues(model, "output").Add(float64(output))
}

// LLMConfigFromEnv reads LLM_BASE_URL, LLM_API_KEY, LLM_MODEL, LLM_ROUTER_MODEL
// and LLM_EXTRA_BODY. OPENROUTER_API_KEY is still read as a fallback key name.
func LLMConfigFromEnv() (LLMConfig, error) {
	cfg := LLMConfig{
		APIKey:      os.Getenv("LLM_API_KEY"),
		BaseURL:     platform.Env("LLM_BASE_URL", "https://openrouter.ai/api/v1"),
		Model:       platform.Env("LLM_MODEL", "deepseek/deepseek-v4.1-flash"),
		RouterModel: platform.Env("LLM_ROUTER_MODEL", "qwen/qwen3.8-27b"),
	}
	if cfg.APIKey == "" {
		// impulse: old key name kept for running deployments, drop it after phase 1 once every .env uses LLM_API_KEY.
		cfg.APIKey = os.Getenv("OPENROUTER_API_KEY")
	}
	if err := json.Unmarshal([]byte(platform.Env("LLM_EXTRA_BODY", defaultExtraBody)), &cfg.ExtraBody); err != nil {
		return LLMConfig{}, fmt.Errorf("LLM_EXTRA_BODY must be a JSON object: %w", err)
	}
	return cfg, nil
}

// NewLLM returns nil when no API key is set.
func NewLLM(cfg LLMConfig) *LLM {
	if cfg.APIKey == "" {
		return nil
	}
	opts := []option.RequestOption{
		option.WithAPIKey(cfg.APIKey),
		option.WithBaseURL(cfg.BaseURL),
		option.WithRequestTimeout(60 * time.Second),
		option.WithMaxRetries(2),
	}
	// By default OpenRouter provider routing that forbids storing prompts.
	for k, v := range cfg.ExtraBody {
		opts = append(opts, option.WithJSONSet(k, v))
	}
	client := openai.NewClient(opts...)
	return &LLM{client: client, model: cfg.Model, routerModel: cfg.RouterModel}
}

// Complete returns one non-streamed answer from the light router model.
func (l *LLM) Complete(ctx context.Context, system, user string) (out string, err error) {
	messages := []openai.ChatCompletionMessageParamUnion{openai.SystemMessage(system), openai.UserMessage(user)}
	ctx, span := startGeneration(ctx, "router-llm", l.routerModel, messages)
	start := time.Now()
	defer func() { endSpan(span, out, err); observeLLM("router", l.routerModel, start, err) }()

	resp, err := l.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:       l.routerModel,
		Temperature: openai.Float(0),
		Messages:    messages,
	})
	if err != nil {
		return "", err
	}
	span.SetAttributes(tracing.InputTokens.Int64(resp.Usage.PromptTokens), tracing.OutputTokens.Int64(resp.Usage.CompletionTokens))
	countTokens(l.routerModel, resp.Usage.PromptTokens, resp.Usage.CompletionTokens)
	if len(resp.Choices) == 0 {
		return "", errors.New("router model returned no choices")
	}
	return resp.Choices[0].Message.Content, nil
}

// Chat returns one non-streamed answer of the main model that may call the
// given tools.
func (l *LLM) Chat(ctx context.Context, messages []openai.ChatCompletionMessageParamUnion, tools []openai.ChatCompletionToolUnionParam) (msg openai.ChatCompletionMessage, err error) {
	ctx, span := startGeneration(ctx, "reply-llm-tools", l.model, messages)
	start := time.Now()
	defer func() { endSpan(span, msg.RawJSON(), err); observeLLM("tools", l.model, start, err) }()

	resp, err := l.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:       l.model,
		Temperature: openai.Float(0.4),
		Messages:    messages,
		Tools:       tools,
	})
	if err != nil {
		return openai.ChatCompletionMessage{}, err
	}
	span.SetAttributes(tracing.InputTokens.Int64(resp.Usage.PromptTokens), tracing.OutputTokens.Int64(resp.Usage.CompletionTokens))
	countTokens(l.model, resp.Usage.PromptTokens, resp.Usage.CompletionTokens)
	if len(resp.Choices) == 0 {
		return openai.ChatCompletionMessage{}, errors.New("model returned no choices")
	}
	return resp.Choices[0].Message, nil
}

// Stream streams an answer from the main model, calling onToken for each chunk,
// and returns the full text.
func (l *LLM) Stream(ctx context.Context, messages []openai.ChatCompletionMessageParamUnion, onToken func(string)) (out string, err error) {
	ctx, span := startGeneration(ctx, "reply-llm", l.model, messages)
	start := time.Now()
	defer func() { endSpan(span, out, err); observeLLM("reply", l.model, start, err) }()

	stream := l.client.Chat.Completions.NewStreaming(ctx, openai.ChatCompletionNewParams{
		Model:         l.model,
		Temperature:   openai.Float(0.4),
		Messages:      messages,
		StreamOptions: openai.ChatCompletionStreamOptionsParam{IncludeUsage: openai.Bool(true)},
	})
	defer stream.Close()

	var full []byte
	for stream.Next() {
		chunk := stream.Current()
		// With include_usage the last chunk carries token counts and no choices.
		if u := chunk.Usage; u.TotalTokens > 0 {
			span.SetAttributes(tracing.InputTokens.Int64(u.PromptTokens), tracing.OutputTokens.Int64(u.CompletionTokens))
			countTokens(l.model, u.PromptTokens, u.CompletionTokens)
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		if t := chunk.Choices[0].Delta.Content; t != "" {
			full = append(full, t...)
			onToken(t)
		}
	}
	return string(full), stream.Err()
}

var tracer = otel.Tracer("horkilion/agent")

func startGeneration(ctx context.Context, name, model string, messages []openai.ChatCompletionMessageParamUnion) (context.Context, trace.Span) {
	return tracer.Start(ctx, name, trace.WithAttributes(
		tracing.ObservationType.String("generation"),
		tracing.Model.String(model),
		tracing.Input.String(tracing.JSON(messages)),
	))
}

// startSpan opens a child observation of the given Langfuse type with its input.
func startSpan(ctx context.Context, name, kind string, input any) (context.Context, trace.Span) {
	in, ok := input.(string)
	if !ok {
		in = tracing.JSON(input)
	}
	return tracer.Start(ctx, name, trace.WithAttributes(
		tracing.ObservationType.String(kind),
		tracing.Input.String(in),
	))
}

// endSpan records the output or the error and ends the span.
func endSpan(span trace.Span, output any, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else if s, ok := output.(string); ok {
		span.SetAttributes(tracing.Output.String(s))
	} else {
		span.SetAttributes(tracing.Output.String(tracing.JSON(output)))
	}
	span.End()
}
