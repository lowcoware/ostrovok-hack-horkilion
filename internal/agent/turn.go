package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/openai/openai-go/v3"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	inventoryv1 "horkilion/gen/inventory/v1"
	"horkilion/internal/analytics"
	"horkilion/internal/platform"
	"horkilion/internal/tracing"
)

const replyPrompt = `Ты агент Островка по планированию поездок. Отвечай по-русски, коротко и по делу.
Решения принимай только по плану поездки (JSON ниже). Не выдумывай отели, цены и погоду, которых нет в плане.
Если направления нет, задай один вопрос: куда ехать.
Если есть варианты, предложи до трёх. Для каждого объясни, почему он подходит, чем пришлось пожертвовать и какие есть риски.
Назови допущения из assumptions и предложи их поправить. Сообщи о каждом конфликте из conflicts и о вариантах с over_budget.
Одной фразой скажи, что данные об отелях синтетические. Чтобы забронировать, пользователь нажимает «Забронировать» у отеля.`

const historyLimit = 10

var (
	turnsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "agent_turns_total",
		Help: "Agent turns by result.",
	}, []string{"result"})
	turnDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "agent_turn_duration_seconds",
		Help:    "Duration of an agent turn.",
		Buckets: prometheus.ExponentialBuckets(0.25, 2, 9),
	})
)

// Agent executes turns.
type Agent struct {
	store     *Store
	router    Router
	llm       *LLM
	inventory inventoryv1.InventoryServiceClient
	nc        *nats.Conn
	analytics *analytics.Client
}

func New(store *Store, llm *LLM, inventory inventoryv1.InventoryServiceClient, nc *nats.Conn, an *analytics.Client) *Agent {
	return &Agent{store: store, router: Router{llm: llm}, llm: llm, inventory: inventory, nc: nc, analytics: an}
}

// HandleTurn runs one turn. A returned error means the turn should be retried.
func (a *Agent) HandleTurn(ctx context.Context, msg platform.TurnMessage) (err error) {
	started := time.Now()
	defer func() {
		result := "ok"
		if err != nil {
			result = "error"
		}
		turnsTotal.WithLabelValues(result).Inc()
		turnDuration.Observe(time.Since(started).Seconds())
	}()

	if done, err := a.store.Processed(ctx, msg.MessageID); err != nil {
		return err
	} else if done {
		return nil
	}

	mode := "rules"
	if a.llm != nil {
		mode = "llm"
	}
	ctx, span := tracer.Start(ctx, "agent-turn", trace.WithAttributes(
		tracing.TraceName.String("agent-turn"),
		tracing.SessionID.String(msg.SessionID),
		tracing.TraceTags.StringSlice([]string{mode}),
		tracing.ObservationType.String("agent"),
		tracing.Input.String(msg.Text),
	))
	var reply string
	defer func() { endSpan(span, reply, err) }()

	today := time.Now().UTC().Truncate(24 * time.Hour)
	plan, err := a.store.LoadPlan(ctx, msg.SessionID)
	if err != nil {
		return fmt.Errorf("load plan: %w", err)
	}
	prevStage := plan.Stage
	a.emit(msg.SessionID, platform.Event{Type: "status", Stage: plan.Stage, Text: "разбираю запрос"})

	invalidated := plan.Apply(a.router.Route(ctx, plan, msg.Text, today), today)
	if plan.SearchGate() {
		plan.FillDefaults(today)
		if len(plan.Options) == 0 {
			a.emit(msg.SessionID, platform.Event{Type: "status", Stage: StageSearch, Text: "ищу отели"})
			if err := a.search(ctx, &plan); err != nil {
				return fmt.Errorf("search hotels: %w", err)
			}
		}
		if plan.Weather == nil {
			a.weather(ctx, &plan)
		}
	}
	plan.NextStage()
	a.emit(msg.SessionID, platform.Event{Type: "plan", Plan: plan})

	span.SetAttributes(attribute.String("langfuse.observation.metadata.stage", plan.Stage))
	reply, err = a.reply(ctx, plan, msg, invalidated)
	if err != nil {
		return fmt.Errorf("reply: %w", err)
	}

	if err := a.store.SaveTurn(ctx, &plan, msg.MessageID, msg.Text, reply); err != nil {
		if errors.Is(err, ErrAlreadyProcessed) {
			return nil
		}
		return fmt.Errorf("save turn: %w", err)
	}
	a.emit(msg.SessionID, platform.Event{Type: "plan", Plan: plan})
	a.emit(msg.SessionID, platform.Event{Type: "done", Stage: plan.Stage})

	if plan.Stage != prevStage {
		a.analytics.Track("stage_entered", plan.SessionID, plan.Stage, map[string]any{"from": prevStage})
	}
	if len(invalidated) > 0 {
		a.analytics.Track("plan_rebuilt", plan.SessionID, plan.Stage, map[string]any{"invalidated": invalidated})
	}
	a.analytics.Track("turn_completed", plan.SessionID, plan.Stage, map[string]any{
		"duration_ms": time.Since(started).Milliseconds(), "options": len(plan.Options), "llm": a.llm != nil,
	})
	return nil
}

// EmitError tells the user that a turn failed for good.
func (a *Agent) EmitError(sessionID, message string) {
	a.emit(sessionID, platform.Event{Type: "error", Message: message})
}

func (a *Agent) search(ctx context.Context, plan *TripPlan) (err error) {
	c := plan.Constraints
	req := &inventoryv1.SearchHotelsRequest{
		City: c.Destination,
		Stay: &inventoryv1.Stay{CheckIn: c.CheckIn, Nights: c.Nights, Adults: c.Adults, Children: c.Children, WithPets: c.Pets},
	}
	ctx, span := startSpan(ctx, "inventory.SearchHotels", "tool", req)
	defer func() { endSpan(span, plan.Options, err) }()

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := a.inventory.SearchHotels(ctx, req)
	if err != nil {
		return err
	}
	plan.SetOptions(resp.GetHotels())
	return nil
}

// weather is best effort: a missing forecast never blocks the turn.
func (a *Agent) weather(ctx context.Context, plan *TripPlan) {
	req := &inventoryv1.GetWeatherRequest{City: plan.Constraints.Destination, Date: plan.Constraints.CheckIn}
	ctx, span := startSpan(ctx, "inventory.GetWeather", "tool", req)
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	w, err := a.inventory.GetWeather(ctx, req)
	endSpan(span, w, err)
	if err != nil {
		slog.Warn("weather unavailable", "city", plan.Constraints.Destination, "err", err)
		return
	}
	plan.Weather = &Weather{
		City: w.GetCity(), Date: plan.Constraints.CheckIn, TempMaxC: w.GetTempMaxC(), TempMinC: w.GetTempMinC(),
		PrecipitationMM: w.GetPrecipitationMm(), Source: w.GetSource(),
	}
}

func (a *Agent) reply(ctx context.Context, plan TripPlan, msg platform.TurnMessage, invalidated []string) (string, error) {
	onToken := func(t string) { a.emit(msg.SessionID, platform.Event{Type: "token", Text: t}) }

	if a.llm != nil {
		history, err := a.store.History(ctx, msg.SessionID, historyLimit)
		if err != nil {
			return "", err
		}
		planJSON, _ := json.Marshal(plan)
		system := replyPrompt + "\n\nПлан поездки: " + string(planJSON)
		if len(invalidated) > 0 {
			system += "\nЧто изменилось в этом ходе: " + strings.Join(invalidated, "; ")
		}
		messages := []openai.ChatCompletionMessageParamUnion{openai.SystemMessage(system)}
		for _, m := range history {
			if m.Role == "assistant" {
				messages = append(messages, openai.AssistantMessage(m.Content))
			} else {
				messages = append(messages, openai.UserMessage(m.Content))
			}
		}
		messages = append(messages, openai.UserMessage(msg.Text))

		ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		text, err := a.llm.Stream(ctx, messages, onToken)
		if err == nil {
			return text, nil
		}
		slog.Warn("llm reply failed, falling back to template", "err", err)
	}

	_, span := startSpan(ctx, "reply-template", "span", plan)
	text := templateReply(plan, invalidated)
	endSpan(span, text, nil)
	for _, word := range strings.SplitAfter(text, " ") {
		onToken(word)
	}
	return text, nil
}

// templateReply answers without an LLM, from the plan alone.
func templateReply(plan TripPlan, invalidated []string) string {
	if !plan.SearchGate() {
		return "Куда хотите поехать? Назовите город, а даты и состав гостей я предположу сам и покажу, что можно поправить."
	}
	c := plan.Constraints
	var b strings.Builder
	for _, s := range invalidated {
		fmt.Fprintf(&b, "Учла изменение: %s.\n", s)
	}
	fmt.Fprintf(&b, "%s, заезд %s, %d ноч., взрослых: %d", c.Destination, c.CheckIn, c.Nights, c.Adults)
	if c.Children > 0 {
		fmt.Fprintf(&b, ", детей: %d", c.Children)
	}
	if c.Pets {
		b.WriteString(", с питомцем")
	}
	b.WriteString(".\n")
	if w := plan.Weather; w != nil {
		label := "прогноз"
		if w.Source == "last_year" {
			label = "в этот день год назад"
		}
		fmt.Fprintf(&b, "Погода (%s): от %.0f до %.0f °C, осадки %.1f мм.\n", label, w.TempMinC, w.TempMaxC, w.PrecipitationMM)
	}
	if len(plan.Options) == 0 {
		b.WriteString("Подходящих отелей не нашла. Попробуйте изменить даты или условия.")
		return b.String()
	}
	b.WriteString("Лучшие варианты:\n")
	for i, o := range plan.Options[:min(3, len(plan.Options))] {
		fmt.Fprintf(&b, "%d. %s, %d★, рейтинг %.1f, %s, %d ₽ за всё", i+1, o.Name, o.Stars, o.Rating, o.Meal, o.TotalPriceRub)
		if o.FreeCancellation {
			b.WriteString(", бесплатная отмена")
		} else {
			b.WriteString(", без бесплатной отмены (риск)")
		}
		if o.OverBudget {
			b.WriteString(", выше бюджета")
		}
		b.WriteString(".\n")
	}
	for _, a := range plan.Assumptions {
		fmt.Fprintf(&b, "Допущение: %s = %s (%s). Можно поправить.\n", a.Field, a.Value, a.Reason)
	}
	for _, cf := range plan.Conflicts {
		fmt.Fprintf(&b, "Внимание: %s.\n", cf.Message)
	}
	b.WriteString("Данные об отелях синтетические.")
	return b.String()
}

func (a *Agent) emit(sessionID string, ev platform.Event) {
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	if err := a.nc.Publish(platform.SessionEventsSubject(sessionID), data); err != nil {
		slog.Warn("publish event failed", "session_id", sessionID, "err", err)
	}
}
