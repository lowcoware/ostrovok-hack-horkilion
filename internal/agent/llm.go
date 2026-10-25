package agent

import (
	"context"
	"errors"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"horkilion/internal/tracing"
)

// LLM talks to OpenRouter (or any OpenAI-compatible endpoint, such as a
// self-hosted vLLM) through the official client. A nil *LLM means no API key is
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
}

// NewLLM returns nil when no API key is set.
func NewLLM(cfg LLMConfig) *LLM {
	if cfg.APIKey == "" {
		return nil
	}
	client := openai.NewClient(
		option.WithAPIKey(cfg.APIKey),
		option.WithBaseURL(cfg.BaseURL),
		option.WithRequestTimeout(60*time.Second),
		option.WithMaxRetries(2),
		// OpenRouter provider routing: providers must not store prompts.
		option.WithJSONSet("provider", map[string]any{"data_collection": "deny"}),
	)
	return &LLM{client: client, model: cfg.Model, routerModel: cfg.RouterModel}
}

// Complete returns one non-streamed answer from the light router model.
func (l *LLM) Complete(ctx context.Context, system, user string) (out string, err error) {
	messages := []openai.ChatCompletionMessageParamUnion{openai.SystemMessage(system), openai.UserMessage(user)}
	ctx, span := startGeneration(ctx, "router-llm", l.routerModel, messages)
	defer func() { endSpan(span, out, err) }()

	resp, err := l.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:       l.routerModel,
		Temperature: openai.Float(0),
		Messages:    messages,
	})
	if err != nil {
		return "", err
	}
	span.SetAttributes(tracing.InputTokens.Int64(resp.Usage.PromptTokens), tracing.OutputTokens.Int64(resp.Usage.CompletionTokens))
	if len(resp.Choices) == 0 {
		return "", errors.New("router model returned no choices")
	}
	return resp.Choices[0].Message.Content, nil
}

// Stream streams an answer from the main model, calling onToken for each chunk,
// and returns the full text.
func (l *LLM) Stream(ctx context.Context, messages []openai.ChatCompletionMessageParamUnion, onToken func(string)) (out string, err error) {
	ctx, span := startGeneration(ctx, "reply-llm", l.model, messages)
	defer func() { endSpan(span, out, err) }()

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
