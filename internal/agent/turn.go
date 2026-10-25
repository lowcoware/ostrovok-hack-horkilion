// Package agent runs agent turns: it routes a user message (or takes a plan
// patch from the interface), applies it to the TripPlan with invalidation,
// moves the plan through stage gates, calls the suppliers and streams the reply.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/openai/openai-go/v3"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	catalogv1 "horkilion/gen/catalog/v1"
	lodgingv1 "horkilion/gen/lodging/v1"
	transferv1 "horkilion/gen/transfer/v1"
	transportv1 "horkilion/gen/transport/v1"
	"horkilion/internal/agent/plan"
	"horkilion/internal/analytics"
	"horkilion/internal/platform"
	"horkilion/internal/tracing"
)

const replyPrompt = `Ты агент Островка по планированию поездок. Отвечай по-русски, коротко и по делу.
Решения принимай только по плану поездки (JSON ниже). Не выдумывай отели, цены и погоду, которых нет в плане.
Если направления нет, а в destination_options есть варианты, предложи их: для каждого почему подходит (explanation.why), чем придётся пожертвовать (tradeoffs), риски (risks), нужна ли виза (destination.visa), и спроси одним вопросом, куда ехать. Если вариантов нет, задай один вопрос: куда ехать.
Варианты отелей лежат в components.stay.options. Предложи до трёх. Для каждого объясни, почему он подходит, чем пришлось пожертвовать и какие есть риски.
Дорога туда и обратно лежит в components.outbound и components.return, трансферы — в components.transfer_arrival и components.transfer_departure. Пока пользователь не выбрал, поездка собрана из первого варианта каждого компонента: назови его и скажи, что можно выбрать другой.
Назови допущения из assumptions и предложи их поправить. Сообщи о каждом конфликте из conflicts, о каждой проверке из checks со статусом warn или fail (в том числе о визе) и о вариантах с флагом over_budget.
Если у данных в sources есть kind "synthetic", одной фразой скажи, что это синтетические данные. Чтобы оформить поездку, пользователь выбирает отель и нажимает «Оформить».`

const historyLimit = 10

var cityExamples = []string{"Сочи", "Казань", "Калининград"}

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
	rules     Rules
	lodging   lodgingv1.LodgingServiceClient
	catalog   catalogv1.CatalogServiceClient
	transport transportv1.TransportServiceClient
	transfer  transferv1.TransferServiceClient
	nc        *nats.Conn
	analytics *analytics.Client
}

// Suppliers are the inventory clients the agent calls.
type Suppliers struct {
	Lodging   lodgingv1.LodgingServiceClient
	Catalog   catalogv1.CatalogServiceClient
	Transport transportv1.TransportServiceClient
	Transfer  transferv1.TransferServiceClient
}

func New(store *Store, llm *LLM, rules Rules, s Suppliers, nc *nats.Conn, an *analytics.Client) *Agent {
	return &Agent{store: store, router: Router{llm: llm}, llm: llm, rules: rules, lodging: s.Lodging, catalog: s.Catalog,
		transport: s.Transport, transfer: s.Transfer, nc: nc, analytics: an}
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
		tracing.Input.String(turnInput(msg)),
	))
	var reply string
	defer func() { endSpan(span, reply, err) }()

	ev := a.events(msg)
	today := time.Now().UTC().Truncate(24 * time.Hour)
	p, err := a.store.LoadPlan(ctx, msg.SessionID)
	if err != nil {
		return fmt.Errorf("load plan: %w", err)
	}
	prevStage := p.Stage
	ev.emit("status", platform.StatusData{Stage: p.Stage, Text: "разбираю запрос"})

	var patch plan.Patch
	userText := msg.Text
	if len(msg.Patch) > 0 {
		// The gateway validated the patch against the schema; a broken one here is a bug, not a retry case.
		dec := json.NewDecoder(bytes.NewReader(msg.Patch))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&patch); err != nil {
			slog.Error("malformed plan patch, dropping the turn", "session_id", msg.SessionID, "err", err)
			ev.emit("error", platform.ErrorData{Code: "bad_request", Message: "Не удалось применить правку плана."})
			return nil
		}
		userText = "Правка плана: " + string(msg.Patch)
	} else {
		patch = a.router.Route(ctx, p, msg.Text, today)
		a.resolvePlaces(ctx, &patch)
	}
	changes := p.Apply(patch, today)

	if err := a.advance(ctx, &p, ev, today); err != nil {
		return err
	}
	ev.emit("plan", p)
	switch p.Stage {
	case plan.StageClarify:
		ev.emit("clarify", platform.ClarifyData{Question: "Куда хотите поехать?", QuickReplies: cityExamples})
	case plan.StageInspire:
		names := make([]string, 0, len(p.DestinationOptions))
		for _, o := range p.DestinationOptions {
			names = append(names, o.Destination.Name)
		}
		ev.emit("clarify", platform.ClarifyData{Question: "Куда из этого поедем?", QuickReplies: names})
	}

	span.SetAttributes(attribute.String("langfuse.observation.metadata.stage", p.Stage))
	reply, err = a.reply(ctx, p, msg, userText, changes, ev)
	if err != nil {
		return fmt.Errorf("reply: %w", err)
	}

	if err := a.store.SaveTurn(ctx, &p, msg.MessageID, userText, reply); err != nil {
		if errors.Is(err, ErrAlreadyProcessed) {
			return nil
		}
		return fmt.Errorf("save turn: %w", err)
	}
	ev.emit("plan", p)
	ev.emit("done", platform.DoneData{Text: reply, Stage: p.Stage})

	if p.Stage != prevStage {
		a.analytics.Track("stage_entered", p.SessionID, p.Stage, map[string]any{"from": prevStage})
	}
	if len(changes) > 0 {
		a.analytics.Track("plan_rebuilt", p.SessionID, p.Stage, map[string]any{"invalidated": changes})
	}
	a.analytics.Track("turn_completed", p.SessionID, p.Stage, map[string]any{
		"duration_ms": time.Since(started).Milliseconds(), "options": len(p.Components.Stay.Options), "llm": a.llm != nil,
		"patch": len(msg.Patch) > 0,
	})
	return nil
}

// advance runs the deterministic part of a turn after the patch: inspire
// without a destination, the trip assembly with one, then the stage gate.
func (a *Agent) advance(ctx context.Context, p *plan.TripPlan, ev *events, today time.Time) error {
	if p.SearchGate() {
		p.FillDefaults(today)
		if err := a.assemble(ctx, p, ev); err != nil {
			return fmt.Errorf("search hotels: %w", err)
		}
		if p.Weather == nil {
			a.weather(ctx, p)
		}
	} else {
		p.FillDefaults(today)
		ev.emit("status", platform.StatusData{Stage: plan.StageInspire, Text: "подбираю направления"})
		// Best effort: without suggestions the agent asks where to go.
		if err := a.inspire(ctx, p); err != nil {
			p.DestinationOptions = []plan.DestinationOption{}
		}
	}
	p.NextStage()
	return nil
}

func turnInput(msg platform.TurnMessage) string {
	if len(msg.Patch) > 0 {
		return string(msg.Patch)
	}
	return msg.Text
}

// EmitError tells the user that a turn failed for good.
func (a *Agent) EmitError(msg platform.TurnMessage, message string) {
	a.events(msg).emit("error", platform.ErrorData{Code: "internal", Message: message})
}

func (a *Agent) searchStay(ctx context.Context, p *plan.TripPlan) (err error) {
	c := p.Constraints
	req := &lodgingv1.SearchHotelsRequest{City: c.Destination, Stay: c.StayProto(), Filters: &lodgingv1.Filters{
		Meal: c.Preferences.Meal, FreeCancellationOnly: c.Preferences.FreeCancellation != nil && *c.Preferences.FreeCancellation,
	}}
	ctx, span := startSpan(ctx, "lodging.SearchHotels", "tool", req)
	defer func() { endSpan(span, p.Components.Stay.Options, err) }()

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := a.lodging.SearchHotels(ctx, req)
	if err != nil {
		return err
	}
	options := make([]plan.Option, 0, len(resp.GetHotels()))
	for _, h := range resp.GetHotels() {
		options = append(options, plan.StayOption(h))
	}
	p.SetOptions(plan.Stay, options)
	return nil
}

// weather is best effort: a missing forecast never blocks the turn.
func (a *Agent) weather(ctx context.Context, p *plan.TripPlan) {
	req := &catalogv1.GetWeatherRequest{City: p.Constraints.Destination, Date: p.Constraints.Dates.Depart}
	ctx, span := startSpan(ctx, "catalog.GetWeather", "tool", req)
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	w, err := a.catalog.GetWeather(ctx, req)
	endSpan(span, w, err)
	if err != nil {
		slog.Warn("weather unavailable", "city", p.Constraints.Destination, "err", err)
		return
	}
	p.Weather = plan.WeatherFromProto(w)
}

func (a *Agent) reply(ctx context.Context, p plan.TripPlan, msg platform.TurnMessage, userText string, changes []string, ev *events) (string, error) {
	onToken := func(t string) { ev.emit("token", platform.TokenData{Text: t}) }

	if a.llm != nil {
		history, err := a.store.History(ctx, msg.SessionID, historyLimit)
		if err != nil {
			return "", err
		}
		planJSON, _ := json.Marshal(p)
		system := replyPrompt + "\n\nПлан поездки: " + string(planJSON)
		if len(changes) > 0 {
			system += "\nЧто изменилось в этом ходе: " + strings.Join(changes, "; ")
		}
		messages := []openai.ChatCompletionMessageParamUnion{openai.SystemMessage(system)}
		for _, m := range history {
			if m.Role == "assistant" {
				messages = append(messages, openai.AssistantMessage(m.Content))
			} else {
				messages = append(messages, openai.UserMessage(m.Content))
			}
		}
		messages = append(messages, openai.UserMessage(userText))

		ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		text, err := a.llm.Stream(ctx, messages, onToken)
		if err == nil {
			return text, nil
		}
		slog.Warn("llm reply failed, falling back to template", "err", err)
	}

	_, span := startSpan(ctx, "reply-template", "span", p)
	text := templateReply(p, changes)
	endSpan(span, text, nil)
	for _, word := range strings.SplitAfter(text, " ") {
		onToken(word)
	}
	return text, nil
}

var mealNames = map[string]string{
	"room_only": "без питания", "breakfast": "завтрак", "half_board": "завтрак и ужин", "all_inclusive": "всё включено",
}

var componentLabels = map[string]string{
	plan.Outbound: "Туда", plan.TransferArrival: "Трансфер до отеля", plan.TransferDeparture: "Трансфер к отправлению", plan.Return: "Обратно",
}

// templateReply answers without an LLM, from the plan alone.
func templateReply(p plan.TripPlan, changes []string) string {
	if !p.SearchGate() && len(p.DestinationOptions) > 0 {
		return inspireReply(p)
	}
	if !p.SearchGate() {
		return "Куда хотите поехать? Назовите город, а даты и состав гостей я предположу сам и покажу, что можно поправить."
	}
	c := p.Constraints
	var b strings.Builder
	for _, s := range changes {
		fmt.Fprintf(&b, "Учла изменение: %s.\n", s)
	}
	fmt.Fprintf(&b, "%s, %s – %s, %d ноч., взрослых: %d", c.Destination, c.Dates.Depart, c.Dates.Return, c.Dates.Nights, c.Adults)
	if len(c.ChildAges) > 0 {
		fmt.Fprintf(&b, ", детей: %d", len(c.ChildAges))
	}
	if c.Pets {
		b.WriteString(", с питомцем")
	}
	b.WriteString(".\n")
	if w := p.Weather; w != nil {
		label := "прогноз"
		if w.Kind == "climate_normal" {
			label = "по прошлым годам"
		}
		fmt.Fprintf(&b, "Погода (%s): от %.0f до %.0f °C, осадки %.1f мм.\n", label, w.TempMinC, w.TempMaxC, w.PrecipitationMM)
	}
	stay := p.Components.Stay
	if len(stay.Options) == 0 {
		b.WriteString("Подходящих отелей не нашла. Попробуйте изменить даты или условия.")
		return b.String()
	}
	if sel := stay.Selected(); sel != nil {
		fmt.Fprintf(&b, "Выбран %s, итого %d ₽. Можно оформлять.\n", sel.Stay.Name, p.TotalRub)
	} else {
		b.WriteString("Лучшие варианты:\n")
		for i, o := range stay.Options[:min(3, len(stay.Options))] {
			h := o.Stay
			fmt.Fprintf(&b, "%d. %s, %d★, рейтинг %.1f, %s, %d ₽ за всё", i+1, h.Name, h.Stars, h.Rating, mealNames[h.Meal], o.PriceRub)
			if h.FreeCancellation {
				b.WriteString(", бесплатная отмена")
			} else {
				b.WriteString(", без бесплатной отмены (риск)")
			}
			if slices.Contains(o.Flags, plan.FlagOverBudget) {
				b.WriteString(", выше бюджета")
			}
			b.WriteString(".\n")
		}
	}
	for _, name := range []string{plan.Outbound, plan.TransferArrival, plan.TransferDeparture, plan.Return} {
		if o := p.Components.Get(name).Effective(); o != nil {
			fmt.Fprintf(&b, "%s: %s, %d ₽.\n", componentLabels[name], plan.OptionTitle(*o), o.PriceRub)
		}
	}
	for _, c := range p.Checks {
		if c.Status != "ok" {
			fmt.Fprintf(&b, "Проверка: %s\n", c.Message)
		}
	}
	for _, a := range p.Assumptions {
		fmt.Fprintf(&b, "Допущение: %s = %s (%s). Можно поправить.\n", a.Field, a.Value, a.Reason)
	}
	for _, cf := range p.Conflicts {
		fmt.Fprintf(&b, "Внимание: %s.\n", cf.Message)
	}
	b.WriteString("Цены и расписания синтетические.")
	return b.String()
}

func inspireReply(p plan.TripPlan) string {
	var b strings.Builder
	b.WriteString("Вот куда можно поехать:\n")
	for i, o := range p.DestinationOptions {
		d, e := o.Destination, o.Explanation
		fmt.Fprintf(&b, "%d. %s, %s. %s", i+1, d.Name, d.Country, e.Why)
		if len(e.Tradeoffs) > 0 {
			fmt.Fprintf(&b, " Минусы: %s.", strings.Join(e.Tradeoffs, ", "))
		}
		if len(e.Risks) > 0 {
			fmt.Fprintf(&b, " Риски: %s.", strings.Join(e.Risks, ", "))
		}
		b.WriteString("\n")
	}
	for _, a := range p.Assumptions {
		fmt.Fprintf(&b, "Допущение: %s = %s (%s). Можно поправить.\n", a.Field, a.Value, a.Reason)
	}
	b.WriteString("Куда из этого поедем? Оценки стоимости синтетические.")
	return b.String()
}

// lastSeq makes seq grow within a session: it is a timestamp in microseconds,
// bumped when two events fall into the same microsecond.
// impulse: seq is unique per agent process, store a per-session counter in NATS KV when turns of one session run on several agent replicas.
var lastSeq atomic.Int64

func nextSeq() int64 {
	for {
		now, last := time.Now().UnixMicro(), lastSeq.Load()
		next := max(now, last+1)
		if lastSeq.CompareAndSwap(last, next) {
			return next
		}
	}
}

// events publishes the events of one turn.
type events struct {
	nc        *nats.Conn
	sessionID string
	turnID    string
}

func (a *Agent) events(msg platform.TurnMessage) *events {
	return &events{nc: a.nc, sessionID: msg.SessionID, turnID: msg.MessageID}
}

func (e *events) emit(typ string, data any) {
	raw, err := json.Marshal(platform.Event{Type: typ, SessionID: e.sessionID, TurnID: e.turnID, Seq: nextSeq(), Data: data})
	if err != nil {
		slog.Error("marshal event failed", "type", typ, "err", err)
		return
	}
	if err := e.nc.Publish(platform.SessionEventsSubject(e.sessionID), raw); err != nil {
		slog.Warn("publish event failed", "session_id", e.sessionID, "err", err)
	}
}
