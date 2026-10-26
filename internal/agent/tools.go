package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/openai/openai-go/v3"

	catalogv1 "horkilion/gen/catalog/v1"
	lodgingv1 "horkilion/gen/lodging/v1"
	"horkilion/internal/agent/plan"
)

// maxToolSteps bounds the tool calling loop of one turn (ADR 0001, step 4).
const maxToolSteps = 4

// Tools are read-only for the cart: they look things up for the answer and
// may only add explanations to options already in the plan.
type tool struct {
	def openai.ChatCompletionToolUnionParam
	run func(ctx context.Context, a *Agent, p *plan.TripPlan, args json.RawMessage) (any, error)
}

func function(name, description string, params map[string]any) openai.ChatCompletionToolUnionParam {
	return openai.ChatCompletionFunctionTool(openai.FunctionDefinitionParam{
		Name: name, Description: openai.String(description), Parameters: openai.FunctionParameters(params),
	})
}

func object(props map[string]any, required ...string) map[string]any {
	o := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		o["required"] = required
	}
	return o
}

var (
	strList = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 3}

	toolSearchDestinations = tool{
		def: function("search_destinations", "Ищет направления от города отправления плана на его даты и состав. Не меняет план.", object(map[string]any{
			"tags":             map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"sea", "warm", "mountains", "city", "nature", "kids", "ski"}}},
			"budget_rub":       map[string]any{"type": "integer", "minimum": 1},
			"max_travel_hours": map[string]any{"type": "number", "minimum": 1},
			"visa_free_only":   map[string]any{"type": "boolean"},
		})),
		run: runSearchDestinations,
	}
	toolSearchHotels = tool{
		def: function("search_hotels", "Ищет отели в направлении плана на его даты с фильтрами. Не меняет план.", object(map[string]any{
			"meal":                   map[string]any{"type": "string", "enum": []string{"room_only", "breakfast", "half_board", "all_inclusive"}},
			"min_stars":              map[string]any{"type": "integer", "minimum": 1, "maximum": 5},
			"free_cancellation_only": map[string]any{"type": "boolean"},
			"max_total_rub":          map[string]any{"type": "integer", "minimum": 1},
		})),
		run: runSearchHotels,
	}
	toolVisa = tool{
		def: function("get_visa_requirement", "Визовый режим для гражданства плана в стране.", object(map[string]any{
			"country_code": map[string]any{"type": "string", "pattern": "^[A-Z]{2}$"},
		}, "country_code")),
		run: runVisa,
	}
	toolExplain = tool{
		def: function("explain_options", "Записывает объяснения к вариантам компонента плана: почему подходит, чем жертвуем, риски. Только по данным плана.", object(map[string]any{
			"component": map[string]any{"type": "string", "enum": plan.ComponentNames},
			"items": map[string]any{"type": "array", "maxItems": 3, "items": object(map[string]any{
				"option_id": map[string]any{"type": "string"},
				"why":       map[string]any{"type": "string", "maxLength": 300},
				"tradeoffs": strList,
				"risks":     strList,
			}, "option_id", "why")},
		}, "component", "items")),
		run: runExplain,
	}
)

// stageTools gives each stage only its tools (ADR 0001): inspire looks for
// destinations, the later stages look for hotels and explain options.
func stageTools(stage string) []tool {
	switch stage {
	case plan.StageClarify, plan.StageInspire:
		return []tool{toolSearchDestinations, toolVisa}
	case plan.StageSearch, plan.StageCompare, plan.StageAssemble:
		return []tool{toolSearchHotels, toolVisa, toolExplain}
	}
	return nil
}

// toolLoop lets the model call the stage tools at most maxToolSteps times and
// returns its final text, emitted to the user word by word.
// impulse: with tools the reply waits for the whole answer, stream tool-call deltas when time to the first token matters.
func (a *Agent) toolLoop(ctx context.Context, p *plan.TripPlan, messages []openai.ChatCompletionMessageParamUnion, onToken func(string)) (string, error) {
	tools := stageTools(p.Stage)
	if len(tools) == 0 {
		return a.llm.Stream(ctx, messages, onToken)
	}
	defs := make([]openai.ChatCompletionToolUnionParam, 0, len(tools))
	byName := map[string]tool{}
	for _, t := range tools {
		defs = append(defs, t.def)
		byName[t.def.GetFunction().Name] = t
	}
	for step := 0; step < maxToolSteps; step++ {
		msg, err := a.llm.Chat(ctx, messages, defs)
		if err != nil {
			return "", err
		}
		if len(msg.ToolCalls) == 0 {
			for _, w := range strings.SplitAfter(msg.Content, " ") {
				onToken(w)
			}
			return msg.Content, nil
		}
		messages = append(messages, msg.ToParam())
		for _, call := range msg.ToolCalls {
			messages = append(messages, openai.ToolMessage(a.runTool(ctx, p, byName, call), call.ID))
		}
	}
	// Out of steps: answer with what the tools returned, without tools.
	return a.llm.Stream(ctx, messages, onToken)
}

// runTool executes one call and returns its result as JSON for the model.
// Errors go back to the model as text: it can correct the arguments.
func (a *Agent) runTool(ctx context.Context, p *plan.TripPlan, byName map[string]tool, call openai.ChatCompletionMessageToolCallUnion) string {
	name, args := call.Function.Name, json.RawMessage(call.Function.Arguments)
	ctx, span := startSpan(ctx, "tool."+name, "tool", call.Function.Arguments)
	t, ok := byName[name]
	var out any
	var err error
	if !ok {
		err = fmt.Errorf("unknown tool %q", name)
	} else {
		out, err = t.run(ctx, a, p, args)
	}
	endSpan(span, out, err)
	if err != nil {
		slog.Warn("tool call failed", "tool", name, "err", err)
		raw, _ := json.Marshal(map[string]string{"error": err.Error()})
		return string(raw)
	}
	raw, _ := json.Marshal(out)
	return string(raw)
}

func decode(args json.RawMessage, dst any) error {
	dec := json.NewDecoder(strings.NewReader(string(args)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("bad arguments: %w", err)
	}
	return nil
}

func runSearchDestinations(ctx context.Context, a *Agent, p *plan.TripPlan, args json.RawMessage) (any, error) {
	var in struct {
		Tags           []string `json:"tags"`
		BudgetRub      int64    `json:"budget_rub"`
		MaxTravelHours float64  `json:"max_travel_hours"`
		VisaFreeOnly   *bool    `json:"visa_free_only"`
	}
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	c := p.Constraints
	req := &catalogv1.SearchDestinationsRequest{
		Origin: c.Origin, DepartDate: c.Dates.Depart, ReturnDate: c.Dates.Return, Travelers: c.TravelersProto(),
		Tags: in.Tags, BudgetRub: in.BudgetRub, MaxTravelHours: in.MaxTravelHours, Citizenship: c.Citizenship,
		VisaFreeOnly: c.Preferences.VisaFreeOnly, Limit: plan.MaxDestinationOptions,
	}
	if in.VisaFreeOnly != nil {
		req.VisaFreeOnly = *in.VisaFreeOnly
	}
	found, err := a.searchDestinations(ctx, req)
	if err != nil {
		return nil, err
	}
	type row struct {
		Name        string  `json:"name"`
		Country     string  `json:"country"`
		Visa        string  `json:"visa"`
		EstimateRub int64   `json:"estimated_total_rub"`
		TravelHours float64 `json:"travel_hours"`
		TempMaxC    float64 `json:"temp_max_c,omitempty"`
	}
	out := make([]row, 0, len(found))
	for _, d := range found {
		r := row{Name: d.Name, Country: d.Country, Visa: d.Visa.Status, EstimateRub: d.EstimatedTotalRub, TravelHours: d.TravelHours}
		if d.Weather != nil {
			r.TempMaxC = d.Weather.TempMaxC
		}
		out = append(out, r)
	}
	return map[string]any{"destinations": out}, nil
}

func runSearchHotels(ctx context.Context, a *Agent, p *plan.TripPlan, args json.RawMessage) (any, error) {
	var in struct {
		Meal                 string `json:"meal"`
		MinStars             int32  `json:"min_stars"`
		FreeCancellationOnly bool   `json:"free_cancellation_only"`
		MaxTotalRub          int64  `json:"max_total_rub"`
	}
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	c := p.Constraints
	ctx, cancel := context.WithTimeout(ctx, supplierTimeout)
	defer cancel()
	resp, err := a.lodging.SearchHotels(ctx, &lodgingv1.SearchHotelsRequest{City: c.Destination, Stay: c.StayProto(), Filters: &lodgingv1.Filters{
		Meal: in.Meal, MinStars: in.MinStars, FreeCancellationOnly: in.FreeCancellationOnly, MaxTotalRub: in.MaxTotalRub,
	}})
	if err != nil {
		return nil, err
	}
	type row struct {
		ID               string  `json:"id"`
		Name             string  `json:"name"`
		Stars            int32   `json:"stars"`
		Rating           float64 `json:"rating"`
		Meal             string  `json:"meal"`
		FreeCancellation bool    `json:"free_cancellation"`
		TotalRub         int64   `json:"total_price_rub"`
	}
	out := []row{}
	for _, h := range resp.GetHotels()[:min(len(resp.GetHotels()), 5)] {
		out = append(out, row{h.GetId(), h.GetName(), h.GetStars(), h.GetRating(), h.GetMeal(), h.GetFreeCancellation(), h.GetTotalPriceRub()})
	}
	return map[string]any{"hotels": out, "note": "чтобы предложить отель, пользователь выбирает его в плане"}, nil
}

func runVisa(ctx context.Context, a *Agent, p *plan.TripPlan, args json.RawMessage) (any, error) {
	var in struct {
		CountryCode string `json:"country_code"`
	}
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, supplierTimeout)
	defer cancel()
	v, err := a.catalog.GetVisaRequirement(ctx, &catalogv1.GetVisaRequirementRequest{Citizenship: p.Constraints.Citizenship, CountryCode: in.CountryCode})
	if err != nil {
		return nil, err
	}
	return plan.VisaFromProto(v), nil
}

func runExplain(_ context.Context, _ *Agent, p *plan.TripPlan, args json.RawMessage) (any, error) {
	var in struct {
		Component string `json:"component"`
		Items     []struct {
			OptionID  string   `json:"option_id"`
			Why       string   `json:"why"`
			Tradeoffs []string `json:"tradeoffs"`
			Risks     []string `json:"risks"`
		} `json:"items"`
	}
	if err := decode(args, &in); err != nil {
		return nil, err
	}
	saved := 0
	for _, it := range in.Items[:min(len(in.Items), 3)] {
		e := plan.Explanation{Why: clip(it.Why), Tradeoffs: clipAll(it.Tradeoffs), Risks: clipAll(it.Risks)}
		if e.Why != "" && p.SetExplanation(in.Component, it.OptionID, e) {
			saved++
		}
	}
	if saved == 0 {
		return nil, fmt.Errorf("no option of %q matched; use option ids from the plan", in.Component)
	}
	return map[string]int{"saved": saved}, nil
}

const maxExplanationRunes = 300

func clip(s string) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > maxExplanationRunes {
		s = string([]rune(s)[:maxExplanationRunes])
	}
	return s
}

func clipAll(in []string) []string {
	out := []string{}
	for _, s := range in[:min(len(in), 3)] {
		if s = clip(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
